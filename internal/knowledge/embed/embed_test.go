package embed_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/knowledge/embed"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// request is the shape the client sends, decoded so a test can assert on it.
type request struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions"`
}

// server answers /v1/embeddings with vectors of dims length, recording every request it saw.
// vector i is filled with the value i so a test can tell them apart.
func server(t *testing.T, dims int) (*httptest.Server, *[]request) {
	t.Helper()
	var seen []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %q, want /v1/embeddings", r.URL.Path)
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		seen = append(seen, req)

		var b strings.Builder
		b.WriteString(`{"data":[`)
		for i := range req.Input {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"index":%d,"embedding":[`, i)
			for d := range dims {
				if d > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, "%d", i)
			}
			b.WriteString("]}")
		}
		b.WriteString("]}")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, b.String())
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestEmbed_RequestShapeAndOrder(t *testing.T) {
	srv, seen := server(t, 4)
	c := embed.New(embed.Config{BaseURL: srv.URL + "/", APIKey: "k", Model: "test-model", Dims: 4}, nil)

	vecs, err := c.Embed(t.Context(), []string{"alpha", "beta", "gamma"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("got %d vectors, want 3", len(vecs))
	}
	// Vector i is filled with i, so this checks the mapping input→vector held.
	for i, v := range vecs {
		if len(v) != 4 {
			t.Fatalf("vector %d has %d dimensions, want 4", i, len(v))
		}
		if v[0] != float32(i) {
			t.Errorf("vector %d = %v, want it to belong to input %d", i, v[0], i)
		}
	}

	if len(*seen) != 1 {
		t.Fatalf("%d requests, want 1 — three inputs fit in one batch", len(*seen))
	}
	got := (*seen)[0]
	if got.Model != "test-model" || got.Dimensions != 4 {
		t.Errorf("request = model %q dims %d, want test-model / 4", got.Model, got.Dimensions)
	}
	if strings.Join(got.Input, ",") != "alpha,beta,gamma" {
		t.Errorf("input = %v, want the three texts in order", got.Input)
	}
}

// The provider reports an index per vector because arrival order is not promised. A permuted
// response must still map each vector back to its own input — getting this wrong attaches every
// chunk's vector to a different chunk, and nothing downstream can notice.
func TestEmbed_HonoursTheReportedIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Deliberately reversed: index 1 first.
		fmt.Fprint(w, `{"data":[{"index":1,"embedding":[9,9]},{"index":0,"embedding":[1,1]}]}`)
	}))
	defer srv.Close()

	vecs, err := embed.New(embed.Config{BaseURL: srv.URL, Dims: 2}, nil).Embed(t.Context(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if vecs[0][0] != 1 || vecs[1][0] != 9 {
		t.Errorf("vectors = %v, want the one reported as index 0 first", vecs)
	}
}

// A provider that ignores the dimensions parameter must fail loudly. Accepting the vector would
// fill an index with lengths the rest of the system cannot compare, and the damage would surface
// much later as merely bad search results.
func TestEmbed_WrongDimensionsIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,2,3]}]}`)
	}))
	defer srv.Close()

	_, err := embed.New(embed.Config{BaseURL: srv.URL, Dims: 768}, nil).Embed(t.Context(), []string{"x"})
	if err == nil {
		t.Fatal("a vector of the wrong length was accepted")
	}
	if !strings.Contains(err.Error(), "768") || !strings.Contains(err.Error(), "3") {
		t.Errorf("error = %q, want both the wanted and the received length", err)
	}
}

// Indexing a folder is thousands of chunks; one request each would spend the run on round trips.
func TestEmbed_BatchesLongInputLists(t *testing.T) {
	srv, seen := server(t, 2)
	texts := make([]string, 150) // > 2 batches at 64
	for i := range texts {
		texts[i] = fmt.Sprintf("chunk %d", i)
	}

	vecs, err := embed.New(embed.Config{BaseURL: srv.URL, Dims: 2}, nil).Embed(t.Context(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("got %d vectors for %d inputs", len(vecs), len(texts))
	}
	if len(*seen) != 3 {
		t.Errorf("%d requests for 150 inputs, want 3", len(*seen))
	}
	total := 0
	for _, r := range *seen {
		if len(r.Input) > 64 {
			t.Errorf("a batch carried %d inputs, want at most 64", len(r.Input))
		}
		total += len(r.Input)
	}
	if total != len(texts) {
		t.Errorf("batches carried %d inputs in total, want %d", total, len(texts))
	}
}

// The boundary that matters: indexing sends document text to a third party, so a vault secret that
// reached the corpus must be stopped BEFORE the request rather than embedded and stored forever.
func TestEmbed_EgressScanBlocksBeforeSending(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,1]}]}`)
	}))
	defer srv.Close()

	store := secret.NewStore()
	store.Set("api", []byte("SUPERSECRETVALUE123"))
	c := embed.New(embed.Config{BaseURL: srv.URL, Dims: 2}, secret.NewScanner(store))

	_, err := c.Embed(t.Context(), []string{"harmless", "a note that pasted SUPERSECRETVALUE123 into it"})
	if err == nil {
		t.Fatal("a batch carrying a secret was embedded")
	}
	if called {
		t.Error("the request was sent before the scan refused it")
	}
	// The whole batch is refused, not just the offending chunk: a partial batch would leave the
	// caller believing a document was indexed when part of it silently was not.
	if !strings.Contains(err.Error(), "egress blocked") {
		t.Errorf("error = %q, want it to name the egress block", err)
	}
	// The refusal must not repeat the value it is protecting.
	if strings.Contains(err.Error(), "SUPERSECRETVALUE123") {
		t.Errorf("the error leaked the secret: %v", err)
	}
}

// A gateway's error body is the most useful thing in the failure, so it has to survive into the
// error rather than being flattened to a status code.
func TestEmbed_ServerErrorQuotesTheProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"error":{"message":"no usable keys for that embedding family"}}`)
	}))
	defer srv.Close()

	_, err := embed.New(embed.Config{BaseURL: srv.URL}, nil).Embed(t.Context(), []string{"x"})
	if err == nil {
		t.Fatal("a 502 was treated as success")
	}
	if !strings.Contains(err.Error(), "no usable keys") {
		t.Errorf("error = %q, want the provider's own message", err)
	}
}

func TestEmbed_NoInputsIsNoRequest(t *testing.T) {
	srv, seen := server(t, 2)
	vecs, err := embed.New(embed.Config{BaseURL: srv.URL}, nil).Embed(context.Background(), nil)
	if err != nil || vecs != nil {
		t.Fatalf("Embed(nil) = %v, %v; want nil, nil", vecs, err)
	}
	if len(*seen) != 0 {
		t.Error("an empty input list still reached the provider")
	}
}
