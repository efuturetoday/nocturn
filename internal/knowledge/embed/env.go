package embed

import (
	"fmt"
	"os"
	"strconv"

	"github.com/efuturetoday/nocturn/internal/secret"
)

// FromEnv builds the embedder from the environment, or nil when this process has none. A nil client
// is a supported state, not a failure: the knowledge tool then does not exist, the same way a
// missing live model means a device is simply told there are no spoken sessions.
//
//	NOCTURN_EMBED_BASE_URL   endpoint          falls back to OPENAI_BASE_URL
//	NOCTURN_EMBED_API_KEY    key for it        falls back to OPENAI_API_KEY
//	NOCTURN_EMBED_MODEL      embedding model   defaults to "auto" — NEVER OPENAI_MODEL
//	NOCTURN_EMBED_DIMS       vector length     defaults to DefaultDims
//
// The endpoint and the key fall back because one gateway usually serves both, and making somebody
// repeat the same two values to use a feature is how a feature goes unused.
//
// The MODEL deliberately does not. OPENAI_MODEL names a CHAT model, and a chat model id is not an
// embedding model id — handing one to /v1/embeddings gets an "unknown embedding model" at best, and
// at worst something that answers and means nothing. Inheriting it would look convenient exactly
// until it silently was not, so embeddings are tuned on their own axis: a separate model, and a
// separate dimensionality that the chat side has no opinion about.
func FromEnv(scanner *secret.Scanner) (*Client, error) {
	baseURL := firstNonEmpty(os.Getenv("NOCTURN_EMBED_BASE_URL"), os.Getenv("OPENAI_BASE_URL"))
	apiKey := firstNonEmpty(os.Getenv("NOCTURN_EMBED_API_KEY"), os.Getenv("OPENAI_API_KEY"))
	if baseURL == "" && apiKey == "" {
		return nil, nil
	}

	dims := DefaultDims
	if raw := os.Getenv("NOCTURN_EMBED_DIMS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			// Refused rather than defaulted: a typo here would build an index at a length the operator
			// did not choose, and the only symptom would be search that quietly works less well.
			return nil, fmt.Errorf("NOCTURN_EMBED_DIMS=%q is not a positive number", raw)
		}
		dims = n
	}

	return New(baseURL, apiKey,
		WithModel(os.Getenv("NOCTURN_EMBED_MODEL")), // empty keeps DefaultModel, never the chat model
		WithDims(dims),
		WithScanner(scanner),
	), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
