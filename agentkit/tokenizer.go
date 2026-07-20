package agentkit

import "unicode/utf8"

// Tokenizer estimates the token count of a piece of text. It is a PORT: exact token counts come
// from the provider response (Step.Tokens); a Tokenizer is only for ESTIMATION — either proactive
// pre-send budgeting, or a fallback when an endpoint omits usage so WithTokenLimit still functions.
// Accurate, model-specific tokenizers (e.g. tiktoken) are separate adapter modules; the core ships
// only this interface and a rough dependency-free default.
type Tokenizer interface {
	Count(text string) (int, error)
}

// approxTokenizer is a crude, dependency-free heuristic: ~4 characters per token (a rough average
// for English). Good enough to give WithTokenLimit a working ceiling on endpoints that return no
// usage; NOT accurate for billing. For accuracy, plug in a real tokenizer adapter.
type approxTokenizer struct{}

// ApproxTokenizer returns the built-in rough estimator (~4 chars/token). It never errors.
func ApproxTokenizer() Tokenizer { return approxTokenizer{} }

func (approxTokenizer) Count(text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	return (utf8.RuneCountInString(text) + 3) / 4, nil
}

var _ Tokenizer = approxTokenizer{}
