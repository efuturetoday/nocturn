package knowledge

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/secret"
)

// topicEmbedder is a deliberately dumb stand-in for meaning: a vector with one axis per topic, set
// by which topic words a text contains. Two texts about the same topic point the same way even with
// no words in common, which is the property the semantic half is supposed to have — and the only
// one these tests need.
type topicEmbedder struct {
	topics [][]string // topics[i] holds the words that light up axis i
}

func (e *topicEmbedder) Model() string { return "topic" }
func (e *topicEmbedder) Dims() int     { return len(e.topics) }

func (e *topicEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		low := strings.ToLower(t)
		v := make([]float32, len(e.topics))
		for axis, words := range e.topics {
			for _, w := range words {
				if strings.Contains(low, w) {
					v[axis] += 1
				}
			}
		}
		out[i] = v
	}
	return out, nil
}

func searchFixture(t *testing.T, emb Embedder, files map[string]string) *Store {
	t.Helper()
	s, corpus := storeFixture(t, emb)
	for rel, body := range files {
		write(t, corpus, rel, body)
	}
	if _, err := s.Index(t.Context()); err != nil {
		t.Fatalf("Index: %v", err)
	}
	return s
}

func paths(rs []Result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Path
	}
	return out
}

// The reason the semantic half exists: a passage that says the same thing in different words.
func TestSearch_FindsAParaphrase(t *testing.T) {
	emb := &topicEmbedder{topics: [][]string{
		{"boat", "sail", "harbour", "vessel", "ship"},
		{"bread", "flour", "oven", "bake", "dough"},
	}}
	s := searchFixture(t, emb, map[string]string{
		"sailing.md": "# Sailing\n\nThe vessel left the harbour under sail at dawn.\n",
		"baking.md":  "# Baking\n\nThe dough rests before it goes in the oven.\n",
	})

	got, err := s.Search(t.Context(), "how do I get a boat out of port", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) == 0 || got[0].Path != "sailing.md" {
		t.Errorf("results = %v, want sailing.md first — no word of the query is in it", paths(got))
	}
}

// The reason the lexical half exists: an identifier the embedding model never saw. The topic
// embedder is blind to it by construction, exactly as a real model is blind to a serial number.
func TestSearch_FindsAnExactIdentifierTheVectorsCannotSee(t *testing.T) {
	emb := &topicEmbedder{topics: [][]string{{"bread", "flour", "oven"}}}
	s := searchFixture(t, emb, map[string]string{
		"baking.md": "# Baking\n\nFlour, an oven, and bread.\n",
		"config.md": "# Config\n\nSet NOCTURN_EMBED_DIMS to 768 before the first index run.\n",
	})

	got, err := s.Search(t.Context(), "NOCTURN_EMBED_DIMS", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) == 0 || got[0].Path != "config.md" {
		t.Errorf("results = %v, want config.md — only the words can find it", paths(got))
	}
	if got[0].Lexical == 0 {
		t.Error("the hit is not credited to the lexical ranker, so it was found by accident")
	}
}

// What fusion buys: agreement wins. A passage both rankers like beats one that only a single ranker
// put first.
func TestSearch_AgreementOutranksASingleRanker(t *testing.T) {
	emb := &topicEmbedder{topics: [][]string{{"harbour", "vessel", "sail"}}}
	s := searchFixture(t, emb, map[string]string{
		"both.md":      "# Harbour\n\nThe vessel is moored in the harbour and will sail at dawn.\n",
		"wordsonly.md": "# Notes\n\nharbour harbour harbour, said the parrot, meaninglessly.\n",
	})

	got, err := s.Search(t.Context(), "vessel in the harbour", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) < 1 || got[0].Path != "both.md" {
		t.Fatalf("results = %v, want both.md first", paths(got))
	}
	if got[0].Semantic == 0 || got[0].Lexical == 0 {
		t.Errorf("the winner was ranked by only one half: semantic=%d lexical=%d", got[0].Semantic, got[0].Lexical)
	}
}

// A result list that reshuffles between two identical queries reads as instability that is not there.
func TestSearch_IsDeterministic(t *testing.T) {
	emb := &topicEmbedder{topics: [][]string{{"alpha"}, {"beta"}}}
	s := searchFixture(t, emb, map[string]string{
		"a.md": "# A\n\nalpha alpha alpha\n",
		"b.md": "# B\n\nbeta beta beta\n",
		"c.md": "# C\n\nneither word appears here at all\n",
	})

	var first []string
	for range 5 {
		got, err := s.Search(t.Context(), "alpha and beta", 3)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if first == nil {
			first = paths(got)
			continue
		}
		if !slices.Equal(first, paths(got)) {
			t.Fatalf("order changed between identical queries: %v then %v", first, paths(got))
		}
	}
}

func TestSearch_RespectsTheLimit(t *testing.T) {
	emb := &topicEmbedder{topics: [][]string{{"thing"}}}
	files := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		files[n+".md"] = "# " + n + "\n\na thing worth finding\n"
	}
	s := searchFixture(t, emb, files)

	got, err := s.Search(t.Context(), "thing", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d results, want 2", len(got))
	}
}

// A document can hold a vault value because somebody pasted it there, and a search result is how it
// would reach the conversation. Same treatment memory_read gives its own notes.
func TestSearch_RedactsVaultValuesFromResults(t *testing.T) {
	store := secret.NewStore()
	store.Set("api", []byte("SUPERSECRETVALUE123"))

	emb := &topicEmbedder{topics: [][]string{{"token", "key"}}}
	s, corpus := storeFixture(t, emb)
	s.scanner = secret.NewScanner(store)
	write(t, corpus, "creds.md", "# Token\n\nThe key is SUPERSECRETVALUE123 and it must not travel.\n")
	if _, err := s.Index(t.Context()); err != nil {
		t.Fatal(err)
	}

	got, err := s.Search(t.Context(), "what is the key", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no result")
	}
	if strings.Contains(got[0].Text, "SUPERSECRETVALUE123") {
		t.Errorf("a search result carried the vault value: %q", got[0].Text)
	}
}

// An empty corpus is an empty answer, not a failure — a workspace nobody has indexed yet is normal.
func TestSearch_EmptyCorpusIsEmpty(t *testing.T) {
	s, _ := storeFixture(t, &topicEmbedder{topics: [][]string{{"x"}}})
	got, err := s.Search(t.Context(), "anything", 5)
	if err != nil {
		t.Fatalf("Search over an empty corpus: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d results from an empty index", len(got))
	}
}

func TestSearch_EmptyQueryIsAnError(t *testing.T) {
	s := searchFixture(t, &topicEmbedder{topics: [][]string{{"x"}}}, map[string]string{"a.md": "# A\n\nx\n"})
	if _, err := s.Search(t.Context(), "   ", 5); err == nil {
		t.Error("an empty query was accepted")
	}
}

// A provider that is down must fail the search rather than quietly degrading it to keyword-only,
// which would look like bad retrieval instead of an outage.
func TestSearch_ProviderFailureIsNotSilentDegradation(t *testing.T) {
	emb := &fakeEmbedder{model: "m", dims: 8}
	s := searchFixture(t, emb, map[string]string{"a.md": "# A\n\nsomething findable\n"})

	emb.setFailOn("findable-query")
	if _, err := s.Search(t.Context(), "findable-query", 5); err == nil {
		t.Error("a failing embedder still returned results")
	}
}

func TestTokenize(t *testing.T) {
	got := tokenize("NOCTURN_EMBED_DIMS=768, set it (once).")
	want := []string{"nocturn", "embed", "dims", "768", "set", "it", "once"}
	if !slices.Equal(got, want) {
		t.Errorf("tokenize = %v, want %v", got, want)
	}
}
