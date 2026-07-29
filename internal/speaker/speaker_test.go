package speaker_test

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/efuturetoday/nocturn/internal/speaker"
)

// modelEnv points at a speaker-embedding checkpoint. It is tens of megabytes and therefore not
// committed, so the tests that need one skip without it.
const modelEnv = "NOCTURN_SPEAKER_MODEL"

func openEmbedder(t *testing.T) *speaker.Embedder {
	t.Helper()
	path := os.Getenv(modelEnv)
	if path == "" {
		t.Skipf("set %s to a WeSpeaker ResNet34 checkpoint to run this test", modelEnv)
	}
	e, err := speaker.Open(path)
	if err != nil {
		t.Fatalf("opening model: %v", err)
	}
	return e
}

// voice is a source-filter caricature of a speaker: a glottal pulse train at pitch, shaped by three
// resonances standing in for a vocal tract. Two voices differing in pitch and formants are the
// crudest thing that is still speaker-LIKE, which is what makes them useful here — pure noise is
// not, because two draws from one random process share a long-term spectrum and a speaker embedding
// is largely a statement about exactly that.
type voice struct {
	pitch    float64
	formants [3]float64
}

// render synthesises one take. Takes of the same voice differ in noise and pitch jitter, the way
// two recordings of one person differ, while the resonances stay put.
func (v voice) render(seed uint64, samples int) []int16 {
	r := rand.New(rand.NewPCG(seed, seed+1))

	// Glottal source: an impulse train with a little jitter, plus breath noise.
	src := make([]float64, samples)
	period := speaker.SampleRate / v.pitch
	for at := 0.0; at < float64(samples); at += period * (1 + 0.02*r.NormFloat64()) {
		src[int(at)] += 1
	}
	for i := range src {
		src[i] += 0.01 * r.NormFloat64()
	}

	// Vocal tract: three two-pole resonators in series, y[n] = x[n] + 2r·cos(ω)y[n-1] − r²y[n-2].
	const bandwidth = 0.97
	out := src
	for _, f := range v.formants {
		w := 2 * math.Pi * f / speaker.SampleRate
		a1, a2 := 2*bandwidth*math.Cos(w), bandwidth*bandwidth
		next := make([]float64, samples)
		var y1, y2 float64
		for i := range out {
			y := out[i] + a1*y1 - a2*y2
			next[i], y2, y1 = y, y1, y
		}
		out = next
	}

	// Normalise to a comfortable level, well clear of clipping.
	var peak float64
	for _, v := range out {
		peak = math.Max(peak, math.Abs(v))
	}
	pcm := make([]int16, samples)
	if peak == 0 {
		return pcm
	}
	for i, v := range out {
		pcm[i] = int16(v / peak * 12000)
	}
	return pcm
}

// Two clearly different speakers: a low voice with back-vowel resonances, and a high one with
// front-vowel resonances.
var (
	lowVoice  = voice{pitch: 110, formants: [3]float64{620, 1100, 2500}}
	highVoice = voice{pitch: 220, formants: [3]float64{420, 2100, 2900}}
)

func TestSimilarityRejectsMismatchedWidths(t *testing.T) {
	if _, err := speaker.Similarity([]float32{1, 0}, []float32{1, 0, 0}); err == nil {
		t.Error("comparing a 2-dimensional and a 3-dimensional embedding returned no error")
	}
	if _, err := speaker.Similarity(nil, nil); err == nil {
		t.Error("comparing empty embeddings returned no error")
	}
}

func TestSimilarityOfKnownVectors(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b []float32
		want float32
	}{
		{"identical", []float32{1, 0, 0}, []float32{1, 0, 0}, 1},
		{"orthogonal", []float32{1, 0, 0}, []float32{0, 1, 0}, 0},
		{"opposed", []float32{1, 0, 0}, []float32{-1, 0, 0}, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := speaker.Similarity(tc.a, tc.b)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(float64(got-tc.want)) > 1e-6 {
				t.Errorf("similarity = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEmbedRejectsShortAudio(t *testing.T) {
	e := openEmbedder(t)
	if _, err := e.Embed(make([]int16, speaker.SampleRate/2)); err == nil {
		t.Error("embedding half a second of audio returned no error")
	}
}

func TestEmbedIsUnitLengthAndDeterministic(t *testing.T) {
	e := openEmbedder(t)
	pcm := lowVoice.render(1, 3*speaker.SampleRate)

	first, err := e.Embed(pcm)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("embedding is empty")
	}

	var norm float64
	for _, v := range first {
		norm += float64(v) * float64(v)
	}
	if math.Abs(math.Sqrt(norm)-1) > 1e-5 {
		t.Errorf("embedding has length %v, want 1", math.Sqrt(norm))
	}

	// Same input, same vector: the pipeline must carry no hidden state between calls.
	second, err := e.Embed(pcm)
	if err != nil {
		t.Fatal(err)
	}
	same, err := speaker.Similarity(first, second)
	if err != nil {
		t.Fatal(err)
	}
	if 1-same > 1e-6 {
		t.Errorf("embedding the same audio twice gave similarity %v, want 1", same)
	}
}

// TestEmbedSeparatesVoices asserts the property the whole package exists for, in the only form
// available without recordings of real people: two takes of one synthetic voice must land closer
// together than takes of two different ones.
//
// The comparison is relative on purpose. Absolute cosine values depend on the checkpoint and on how
// speech-like the input is, and synthetic vowels are far outside the training distribution — so a
// threshold pinned here would be meaningless. The ORDERING is not: if a wrong Mel scale, sample
// rate or window flattened the frontend, the two voices would stop separating while everything
// still looked like healthy features. Real voices remain the acceptance test; this is the guard
// that fails loudly first.
func TestEmbedSeparatesVoices(t *testing.T) {
	e := openEmbedder(t)
	const duration = 3 * speaker.SampleRate

	embed := func(v voice, seed uint64) []float32 {
		t.Helper()
		emb, err := e.Embed(v.render(seed, duration))
		if err != nil {
			t.Fatal(err)
		}
		return emb
	}
	similarity := func(a, b []float32) float32 {
		t.Helper()
		s, err := speaker.Similarity(a, b)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	lowTakeOne, lowTakeTwo := embed(lowVoice, 1), embed(lowVoice, 2)
	highTakeOne := embed(highVoice, 3)

	same := similarity(lowTakeOne, lowTakeTwo)
	different := similarity(lowTakeOne, highTakeOne)
	t.Logf("same voice %.4f, different voices %.4f, margin %.4f", same, different, same-different)

	if same <= different {
		t.Errorf("two takes of one voice scored %v while two voices scored %v; "+
			"the embedding does not separate speakers", same, different)
	}
}

// TestEmbedIsConcurrencySafe backs the claim in Embedder's documentation. The type is only useful
// if a voice session and a background enrolment can share one instance, and "no mutable state" is
// exactly the kind of assertion that quietly stops being true.
func TestEmbedIsConcurrencySafe(t *testing.T) {
	e := openEmbedder(t)
	pcm := lowVoice.render(1, 2*speaker.SampleRate)

	want, err := e.Embed(pcm)
	if err != nil {
		t.Fatal(err)
	}

	const workers = 4
	errs := make(chan error, workers)
	for range workers {
		go func() {
			got, err := e.Embed(pcm)
			if err != nil {
				errs <- err
				return
			}
			same, err := speaker.Similarity(got, want)
			if err != nil {
				errs <- err
				return
			}
			if 1-same > 1e-6 {
				errs <- fmt.Errorf("concurrent embedding differs, similarity %v", same)
				return
			}
			errs <- nil
		}()
	}
	for range workers {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

func BenchmarkEmbed(b *testing.B) {
	path := os.Getenv(modelEnv)
	if path == "" {
		b.Skipf("set %s to a WeSpeaker ResNet34 checkpoint to run this benchmark", modelEnv)
	}
	e, err := speaker.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	pcm := lowVoice.render(1, 3*speaker.SampleRate)

	b.ResetTimer()
	for b.Loop() {
		if _, err := e.Embed(pcm); err != nil {
			b.Fatal(err)
		}
	}
}
