// Package knowledge is retrieval over a workspace's own documents: the folder you drop things into,
// chunked, embedded, and searched on demand.
//
// It is the counterpart to internal/memory, and the two are deliberately not the same shape.
// Memory is what the assistant chose to remember — small, curated, and folded into EVERY prompt as
// a catalog. Knowledge is what YOU put there — arbitrarily large, never in the prompt, reached only
// when a tool goes looking. One is identity, the other is a library.
//
// That difference decides where each lives. Memory sits outside the file tools' mount, because it is
// control plane: text that reaches every future prompt must not be writable through a generic file
// tool. Knowledge sits INSIDE the mount, at mnt/knowledge/, because documents are data — the same
// argument ADR-10 makes about a self-written skill granting no authority. What comes back from a
// search is untrusted content and is treated as such: redacted on the way in, and marked as foreign
// in the prompt.
//
// The index does NOT live in the mount. It is host state — vectors, hashes, offsets — and the model
// has no business rewriting it.
package knowledge

import "context"

// Embedder turns text into vectors. It is a port: the consumer defines it, and the adapter lives
// beside it in internal/knowledge/embed.
//
// Batched on purpose. Indexing a folder is thousands of chunks, and one request per chunk would
// spend the whole run on round trips; every provider worth using accepts a list.
//
// A vector's meaning is relative to the model that produced it, so an index records which model and
// how many dimensions it was built with. Comparing across models is not a degraded answer, it is a
// meaningless one — see Index.stale.
type Embedder interface {
	// Embed returns one vector per input, in order. An error means none of them were produced.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Model names the embedding model, so an index can refuse vectors it cannot compare.
	Model() string
	// Dims is the length every returned vector has.
	Dims() int
}
