package embed_test

import (
	"testing"

	"github.com/efuturetoday/nocturn/internal/knowledge/embed"
)

// clearEnv unsets every variable FromEnv reads, so a test states its own world completely and does
// not inherit the developer's shell or a .env somebody loaded.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"NOCTURN_EMBED_BASE_URL", "NOCTURN_EMBED_API_KEY", "NOCTURN_EMBED_MODEL", "NOCTURN_EMBED_DIMS",
		"OPENAI_BASE_URL", "OPENAI_API_KEY", "OPENAI_MODEL",
	} {
		t.Setenv(k, "")
	}
}

// Nothing configured is a supported state, not a failure: the knowledge tool then does not exist.
func TestFromEnv_UnconfiguredIsNilAndNoError(t *testing.T) {
	clearEnv(t)
	c, err := embed.FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if c != nil {
		t.Error("a client was built with nothing configured")
	}
}

// The endpoint and key fall back to the chat side, because one gateway usually serves both and
// making somebody repeat two values to use a feature is how a feature goes unused.
func TestFromEnv_InheritsEndpointAndKey(t *testing.T) {
	clearEnv(t)
	t.Setenv("OPENAI_BASE_URL", "https://gateway.example")
	t.Setenv("OPENAI_API_KEY", "chat-key")

	c, err := embed.FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if c == nil {
		t.Fatal("no client although the chat endpoint is configured")
	}
	if got := c.Dims(); got != embed.DefaultDims {
		t.Errorf("dims = %d, want the default %d", got, embed.DefaultDims)
	}
}

// The one inheritance that must NOT happen. OPENAI_MODEL names a chat model, and a chat model id is
// not an embedding model id — /v1/embeddings answers "unknown embedding model" at best, and at worst
// answers with something meaningless. Embeddings are tuned on their own axis.
func TestFromEnv_ModelIsNeverTheChatModel(t *testing.T) {
	clearEnv(t)
	t.Setenv("OPENAI_BASE_URL", "https://gateway.example")
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_MODEL", "gemini-3.5-flash") // a chat model

	c, err := embed.FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if c.Model() == "gemini-3.5-flash" {
		t.Fatal("the chat model was inherited as the embedding model")
	}
	if c.Model() != embed.DefaultModel {
		t.Errorf("model = %q, want the default %q", c.Model(), embed.DefaultModel)
	}
}

func TestFromEnv_EveryFieldIsSeparatelyTunable(t *testing.T) {
	clearEnv(t)
	t.Setenv("OPENAI_BASE_URL", "https://chat.example")
	t.Setenv("OPENAI_API_KEY", "chat-key")
	t.Setenv("NOCTURN_EMBED_BASE_URL", "https://embeddings.example")
	t.Setenv("NOCTURN_EMBED_API_KEY", "embed-key")
	t.Setenv("NOCTURN_EMBED_MODEL", "gemini-embedding-001")
	t.Setenv("NOCTURN_EMBED_DIMS", "1536")

	c, err := embed.FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if c.Model() != "gemini-embedding-001" {
		t.Errorf("model = %q", c.Model())
	}
	if c.Dims() != 1536 {
		t.Errorf("dims = %d, want 1536", c.Dims())
	}
}

// Refused rather than defaulted: a typo would otherwise build the index at a length nobody chose,
// and the only symptom is search that quietly works less well.
func TestFromEnv_BadDimsIsAnError(t *testing.T) {
	for _, bad := range []string{"seven", "0", "-1", "768.5"} {
		t.Run(bad, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("OPENAI_BASE_URL", "https://gateway.example")
			t.Setenv("NOCTURN_EMBED_DIMS", bad)

			if _, err := embed.FromEnv(nil); err == nil {
				t.Errorf("NOCTURN_EMBED_DIMS=%q was accepted", bad)
			}
		})
	}
}
