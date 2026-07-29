// Package speaker answers "who said this" from 16 kHz PCM, entirely on the machine that runs
// nocturn: no CGO, no shared library, no service call, and no audio leaving the device.
//
// It is a two-stage thing. A frozen neural network turns an utterance into a fixed-length vector —
// an embedding — such that two recordings of the same person land close together and two people
// land far apart. Nothing is trained here and nothing learns: enrolling a voice means recording a
// few seconds, keeping the vectors, and later comparing new ones against them by cosine similarity.
// Recognition is therefore a nearest-neighbour question, not a classification, which is why a new
// household member costs one short recording rather than a training run.
//
// # What this is for: telling a household apart
//
// Which notes belong in the prompt, whose preferences apply, who to address. Among a handful of
// enrolled people this works essentially perfectly — over twelve thousand queries, households of two
// to six were identified correctly every time. Being wrong costs a misaddressed sentence, so the
// threshold exists only to notice a visitor or a television. It belongs to a CHANNEL rather than to
// this package: a far-field microphone scores the same voice lower than a close one, so the number
// that suits a laptop turns a household member away in a hallway. See DefaultThreshold, and
// testdata/README.md for both measurements.
//
// A result chooses context and address — whose notes, whose mailbox, what to call someone. It does
// not stand in for an approval: speech is a channel like the chat, where nobody authenticates the
// typist either, and a gated action is confirmed out of band on a second device in both. See
// testdata/README.md if that question comes up in earnest.
//
// Voiceprints are still somebody's, so enrolment should be something a person did on purpose.
package speaker

import (
	"fmt"
	"math"

	"github.com/efuturetoday/nocturn/internal/onnx"
)

// MinSamples is the shortest utterance worth embedding — one second. Below that the embedding is
// dominated by whichever phonemes happen to be present rather than by the speaker, and comparisons
// become noise. Better to decline than to answer badly.
const MinSamples = SampleRate

// Embedder turns speech into a speaker embedding. It holds a parsed model and precomputed
// filterbank tables, and is immutable after Open, so one instance serves concurrent callers.
type Embedder struct {
	graph *onnx.Graph
	bank  *FilterBank
	input string
}

// Open loads a speaker-embedding model from an ONNX file. The checkpoint must take a single
// [batch, frames, MelBins] input, which is the layout WeSpeaker and 3D-Speaker export.
func Open(modelPath string) (*Embedder, error) {
	g, err := onnx.ReadFile(modelPath)
	if err != nil {
		return nil, fmt.Errorf("speaker: loading model: %w", err)
	}
	if len(g.Inputs) != 1 {
		return nil, fmt.Errorf("speaker: model takes %d inputs, expected exactly one", len(g.Inputs))
	}
	if len(g.Outputs) == 0 {
		return nil, fmt.Errorf("speaker: model declares no outputs")
	}
	return &Embedder{graph: g, bank: NewFilterBank(), input: g.Inputs[0]}, nil
}

// Embed returns the L2-normalised embedding of one utterance.
//
// Normalising here rather than at comparison time means every stored profile and every query live
// on the unit sphere, so a cosine similarity is a plain dot product and no caller can forget the
// step. The samples are raw int16 at SampleRate — see FilterBank.Compute on why the scale matters.
func (e *Embedder) Embed(pcm []int16) ([]float32, error) {
	if len(pcm) < MinSamples {
		return nil, fmt.Errorf("speaker: utterance is %d samples, need at least %d (%.0f s)",
			len(pcm), MinSamples, float64(MinSamples)/SampleRate)
	}
	features, err := e.bank.Compute(pcm)
	if err != nil {
		return nil, err
	}

	in := &onnx.Tensor{
		Shape: []int{1, features.Frames, MelBins},
		Data:  features.Data,
	}
	outs, err := e.graph.Run(map[string]*onnx.Tensor{e.input: in})
	if err != nil {
		return nil, fmt.Errorf("speaker: running model: %w", err)
	}

	// Checkpoints that keep their training classifier emit its logits too; the embedding is the
	// output whose width is not the training vocabulary, and it is the narrowest one.
	best := outs[0]
	for _, o := range outs[1:] {
		if o.Size() < best.Size() {
			best = o
		}
	}
	// normalize allocates, so the result never aliases the graph's weights.
	return Normalize(best.Data), nil
}

// Normalize scales a vector to unit length, which is what Similarity assumes of both its arguments.
// Exported because an average of unit vectors is not one, so anyone combining embeddings has to
// finish the job here. A zero vector cannot arise from a real utterance, but returning it unchanged
// beats dividing by zero if one ever does.
func Normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		return append([]float32(nil), v...)
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / norm)
	}
	return out
}

// Similarity is the cosine similarity of two embeddings, in [-1, 1] — higher means more alike.
// Both must come from Embed, and therefore be unit length, which reduces the cosine to a dot
// product. Mismatched widths mean the two came from different models and cannot be compared.
func Similarity(a, b []float32) (float32, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("speaker: embeddings have %d and %d dimensions", len(a), len(b))
	}
	if len(a) == 0 {
		return 0, fmt.Errorf("speaker: embeddings are empty")
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return float32(min(max(dot, -1), 1)), nil
}
