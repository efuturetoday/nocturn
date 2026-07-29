package onnx_test

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/efuturetoday/nocturn/internal/onnx"
)

// modelEnv names the environment variable that points at a speaker-embedding checkpoint. The file
// is tens of megabytes, so it is not committed; what IS committed is the embedding an independent
// implementation produced from it (testdata/*.golden.json), which is the part worth version control.
const modelEnv = "NOCTURN_SPEAKER_MODEL"

type goldenFile struct {
	Model     string    `json:"model"`
	Reference string    `json:"reference"`
	Input     string    `json:"input"`
	Frames    int       `json:"frames"`
	MelBins   int       `json:"mel_bins"`
	Embedding []float32 `json:"embedding"`
}

func loadGolden(t *testing.T, path string) goldenFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("decoding golden file: %v", err)
	}
	return g
}

// syntheticFeatures reproduces the input the golden file documents. It is not speech — it exists to
// pin arithmetic, not accuracy — but it must be bit-identical to what produced the reference.
func syntheticFeatures(frames, melBins int) *onnx.Tensor {
	x := onnx.NewTensor(1, frames, melBins)
	for t := range frames {
		for k := range melBins {
			x.Data[t*melBins+k] = float32(math.Sin(float64(t*melBins+k) * 0.01))
		}
	}
	return x
}

// TestMatchesIndependentImplementation is the check that matters for this package. The reference
// embedding was produced by an unrelated ONNX implementation (recorded in the golden file), so
// agreement means two independent code paths compute the same function — not that this package
// agrees with itself. Cosine similarity is the metric because it is exactly what speaker
// verification compares downstream.
func TestMatchesIndependentImplementation(t *testing.T) {
	path := os.Getenv(modelEnv)
	if path == "" {
		t.Skipf("set %s to a WeSpeaker ResNet34 checkpoint to run this test", modelEnv)
	}
	want := loadGolden(t, "testdata/wespeaker_resnet34.golden.json")

	g, err := onnx.ReadFile(path)
	if err != nil {
		t.Fatalf("reading model: %v", err)
	}
	if len(g.Inputs) != 1 {
		t.Fatalf("model has %d inputs, expected 1", len(g.Inputs))
	}

	x := syntheticFeatures(want.Frames, want.MelBins)
	outs, err := g.Run(map[string]*onnx.Tensor{g.Inputs[0]: x})
	if err != nil {
		t.Fatalf("running model: %v", err)
	}
	got := outs[0].Data
	if len(got) != len(want.Embedding) {
		t.Fatalf("embedding has %d dimensions, reference has %d", len(got), len(want.Embedding))
	}

	var dot, normGot, normWant, maxDiff float64
	for i := range got {
		dot += float64(got[i]) * float64(want.Embedding[i])
		normGot += float64(got[i]) * float64(got[i])
		normWant += float64(want.Embedding[i]) * float64(want.Embedding[i])
		maxDiff = math.Max(maxDiff, math.Abs(float64(got[i]-want.Embedding[i])))
	}
	cosine := dot / (math.Sqrt(normGot) * math.Sqrt(normWant))
	t.Logf("cosine similarity %.10f, largest single difference %.3g", cosine, maxDiff)

	// Float32 accumulation in a different order is the only difference these two may have.
	if 1-cosine > 1e-9 {
		t.Errorf("cosine similarity %.10f against the reference is too low", cosine)
	}
	if maxDiff > 1e-4 {
		t.Errorf("largest single difference %g against the reference is too large", maxDiff)
	}
}

func BenchmarkSpeakerEmbedding(b *testing.B) {
	path := os.Getenv(modelEnv)
	if path == "" {
		b.Skipf("set %s to a WeSpeaker ResNet34 checkpoint to run this benchmark", modelEnv)
	}
	g, err := onnx.ReadFile(path)
	if err != nil {
		b.Fatalf("reading model: %v", err)
	}
	// 300 frames at a 10 ms hop is three seconds of speech, a typical enrolment or query window.
	x := syntheticFeatures(300, 80)
	in := map[string]*onnx.Tensor{g.Inputs[0]: x}

	b.ResetTimer()
	for b.Loop() {
		if _, err := g.Run(in); err != nil {
			b.Fatal(err)
		}
	}
}
