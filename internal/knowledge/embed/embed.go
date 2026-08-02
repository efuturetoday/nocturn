// Package embed is the remote half of knowledge retrieval: an OpenAI-compatible /v1/embeddings
// client, and the only place in the tree that sends document text to a third party.
//
// That last sentence is the reason this is its own package with its own boundary. Indexing a folder
// means every document in it leaves the machine, which is a privacy statement the operator has to
// be able to make deliberately — so the egress leak scanner runs HERE, before the request, on the
// same principle as internal/tools/net.go and internal/mcp/conn.go: a secret that reached the
// corpus is blocked at the boundary rather than embedded and stored forever.
//
// There is no local alternative today. internal/onnx runs a convolutional speaker network in pure
// Go, and a sentence-transformer needs a transformer's operator set plus a tokenizer; the honest
// position is a port with a remote adapter behind it, and a local adapter later.
package embed

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/efuturetoday/nocturn/internal/knowledge"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// DefaultDims is what an index is built with unless told otherwise.
//
// Not the model's maximum, on purpose. A provider that offers 3072 offers it for corpora far larger
// than one person's documents, and the cost is carried in every byte of the index and every
// comparison: at 3072 a chunk's vector is 12 KB, four times what 768 costs, for a difference in
// recall that a few thousand chunks cannot show. Providers that support the dimensions parameter
// train for this (Matryoshka representations), so a shorter vector is a truncation the model
// expects rather than a lossy afterthought.
const DefaultDims = 768

// DefaultModel matches what the chat side does with an unset model: let the endpoint choose. A
// gateway that fronts several providers is the normal case here, and "auto" is how it is asked.
const DefaultModel = "auto"

// maxBatch bounds one request. Providers cap the input list and the total tokens; a fixed, modest
// batch is simpler than discovering each provider's limit and degrades to more requests rather than
// to a failure.
const maxBatch = 64

var _ knowledge.Embedder = (*Client)(nil)

// Client is an OpenAI-compatible embeddings endpoint.
type Client struct {
	http    *http.Client
	baseURL string
	apiKey  string
	model   string
	dims    int
	scanner *secret.Scanner
}

// Config is where to embed and how. It is resolved by the process that owns configuration —
// cmd/nocturn, from the environment — rather than read here: a library that reaches for os.Getenv
// cannot be handed a second endpoint, and cannot be tested without a shell.
//
// The zero Model and Dims mean the defaults above, so a caller that has nothing to say says nothing.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Dims    int
}

// Configured reports whether there is an endpoint to talk to at all. Unconfigured is a supported
// state — the knowledge tool then simply does not exist.
func (c Config) Configured() bool { return c.BaseURL != "" || c.APIKey != "" }

// Option configures a Client. Only the HTTP client is one, for tests; everything else is Config.
type Option func(*Client)

// WithHTTPClient replaces the HTTP client, for tests.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// New builds a client. A trailing slash on the base URL is tolerated.
//
// The scanner is a parameter rather than an option because it is not optional in spirit: this is
// the boundary documents cross on their way off the machine, and it belongs to the workspace whose
// vault the secrets are in — which is why the client is built per workspace and not once per
// process.
func New(cfg Config, scanner *secret.Scanner, opts ...Option) *Client {
	c := &Client{
		// Generous: a batch of long chunks against a busy gateway is slow, and indexing is not
		// interactive. The caller's context is what actually bounds a run.
		http:    &http.Client{Timeout: 2 * time.Minute},
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   cmp.Or(cfg.Model, DefaultModel),
		dims:    DefaultDims,
		scanner: scanner,
	}
	if cfg.Dims > 0 {
		c.dims = cfg.Dims
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Model names the embedding model an index was built with.
func (c *Client) Model() string { return c.model }

// Dims is the vector length this client produces.
func (c *Client) Dims() int { return c.dims }

// Embed returns one vector per input, in order.
//
// Inputs are scanned for vault secrets BEFORE the request, and a hit refuses the whole batch rather
// than dropping the offending chunk: a partial batch would leave the caller believing a document
// was indexed when part of it silently was not.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if c.scanner != nil {
		if err := c.scanner.ScanEgress(texts...); err != nil {
			return nil, fmt.Errorf("embed: egress blocked: %w", err)
		}
	}

	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += maxBatch {
		end := min(start+maxBatch, len(texts))
		vecs, err := c.batch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// embedRequest is the wire request. dimensions is omitted when zero so a provider that does not
// know the field is not handed one.
type embedRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// batch sends one request and returns its vectors, ordered by the index the provider reports.
func (c *Client) batch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Model: c.model, Input: texts, Dimensions: c.dims})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	defer resp.Body.Close()

	// Bounded: a gateway erroring with an HTML page should not be read into memory whole.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("embed: reading response: %w", err)
	}

	var parsed embedResponse
	// A non-2xx may still carry a JSON error worth quoting, so decode before judging the status.
	jsonErr := json.Unmarshal(raw, &parsed)
	if resp.StatusCode/100 != 2 {
		if jsonErr == nil && parsed.Error != nil && parsed.Error.Message != "" {
			return nil, fmt.Errorf("embed: %s: %s", resp.Status, parsed.Error.Message)
		}
		return nil, fmt.Errorf("embed: %s: %s", resp.Status, snippet(raw))
	}
	if jsonErr != nil {
		return nil, fmt.Errorf("embed: response is not JSON: %w", jsonErr)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("embed: asked for %d vectors, got %d", len(texts), len(parsed.Data))
	}

	// Order by the reported index rather than by arrival: the field exists because the two are not
	// promised to agree, and a silently permuted batch attaches every vector to the wrong chunk.
	out := make([][]float32, len(parsed.Data))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("embed: vector index %d out of range", d.Index)
		}
		if out[d.Index] != nil {
			return nil, fmt.Errorf("embed: vector index %d repeated", d.Index)
		}
		// Checked, not trusted. A provider that ignores the dimensions parameter would otherwise fill
		// an index with vectors the rest of the system cannot compare, and the damage only shows up
		// as bad search results much later.
		if len(d.Embedding) != c.dims {
			return nil, fmt.Errorf("embed: asked for %d dimensions, got %d — set NOCTURN_EMBED_DIMS to what this model returns",
				c.dims, len(d.Embedding))
		}
		out[d.Index] = d.Embedding
	}
	for i, v := range out {
		if v == nil {
			return nil, fmt.Errorf("embed: no vector for input %d", i)
		}
	}
	return out, nil
}

// snippet trims a response body down to something quotable in an error.
func snippet(b []byte) string {
	const limit = 200
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "(empty response)"
	}
	if len(s) > limit {
		s = s[:limit] + "…"
	}
	return strconv.Quote(s)
}
