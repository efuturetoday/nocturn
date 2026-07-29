package speaker

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// goldenFbank is the reference torchaudio produced; see testdata/kaldi_fbank.golden.json for the
// settings it was generated with and scratchpad tooling for the generator.
type goldenFbank struct {
	Reference  string    `json:"reference"`
	Settings   string    `json:"settings"`
	PCMSeed    int64     `json:"pcm_seed"`
	PCMSamples int       `json:"pcm_samples"`
	Frames     int       `json:"frames"`
	MelBins    int       `json:"mel_bins"`
	Features   []float32 `json:"features"`
}

// referencePCM reproduces the generator the reference used. It is an integer linear congruential
// generator on purpose: a floating-point test signal would differ in its last bits between Python
// and Go, and that difference would show up in the comparison as if the filterbank were at fault.
func referencePCM(seed int64, n int) []int16 {
	pcm := make([]int16, n)
	x := seed
	for i := range pcm {
		x = (1103515245*x + 12345) & 0x7FFFFFFF
		pcm[i] = int16((x>>8)&0xFFFF - 32768)
	}
	return pcm
}

// TestMatchesTorchaudio is the acceptance test for the frontend.
//
// Every other test in this package checks a property that a wrong-but-plausible filterbank would
// also satisfy — the window has the right shape, a tone lands in the right Mel bin, gain is absorbed.
// None of them would notice a Hamming window where Povey belongs, a 0.97 pre-emphasis applied in the
// wrong order, or a log floor at the wrong value. Those produce features that look entirely healthy
// and quietly cost accuracy, because the network was trained on something slightly different.
//
// The only way to rule that out is to compare against the implementation the checkpoints were
// actually trained with. That comparison happens here, once, against a recorded result.
func TestMatchesTorchaudio(t *testing.T) {
	raw, err := os.ReadFile("testdata/kaldi_fbank.golden.json")
	if err != nil {
		t.Fatalf("reading the reference: %v", err)
	}
	var want goldenFbank
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("decoding the reference: %v", err)
	}
	t.Logf("reference: %s", want.Reference)

	if want.MelBins != MelBins {
		t.Fatalf("reference has %d Mel bins, this package computes %d", want.MelBins, MelBins)
	}

	got, err := NewFilterBank().Compute(referencePCM(want.PCMSeed, want.PCMSamples))
	if err != nil {
		t.Fatalf("computing features: %v", err)
	}
	if got.Frames != want.Frames {
		t.Fatalf("computed %d frames, reference has %d", got.Frames, want.Frames)
	}
	if len(got.Data) != len(want.Features) {
		t.Fatalf("computed %d values, reference has %d", len(got.Data), len(want.Features))
	}

	// The reference accumulates in float32 and this package in float64, and the reference was
	// rounded to six decimals on the way to disk, so the two cannot agree exactly. They must agree
	// far more closely than any of the differences a wrong setting would introduce: swapping the
	// window alone moves coefficients by whole units.
	const tolerance = 1e-3
	var worst float64
	worstAt := -1
	for i := range want.Features {
		if d := math.Abs(float64(got.Data[i] - want.Features[i])); d > worst {
			worst, worstAt = d, i
		}
	}
	if worst > tolerance {
		frame, bin := worstAt/MelBins, worstAt%MelBins
		t.Errorf("largest difference %g at frame %d bin %d (computed %v, reference %v), tolerance %g",
			worst, frame, bin, got.Data[worstAt], want.Features[worstAt], tolerance)
	}
	t.Logf("largest difference from the reference: %.3g over %d values", worst, len(want.Features))

	// A golden test is worth exactly as much as its ability to fail. Recompute with Kaldi's
	// default window — the nearest plausible wrong answer, since it is what the documentation
	// recommends — and require the comparison to reject it by a wide margin.
	t.Run("rejects the wrong window", func(t *testing.T) {
		wrong := &FilterBank{
			window:  poveyWindow(frameLength),
			melBank: melFilters(),
			twiddle: twiddles(fftSize),
		}
		features, err := wrong.Compute(referencePCM(want.PCMSeed, want.PCMSamples))
		if err != nil {
			t.Fatal(err)
		}
		var diff float64
		for i := range want.Features {
			diff = math.Max(diff, math.Abs(float64(features.Data[i]-want.Features[i])))
		}
		t.Logf("a Povey window instead of Hamming shifts coefficients by up to %.3g", diff)
		if diff <= tolerance {
			t.Errorf("the wrong window differs by only %g, within the %g tolerance; "+
				"this comparison cannot detect a wrong frontend", diff, tolerance)
		}
	})
}

// poveyWindow exists only so the golden test can demonstrate that it detects a wrong window. It is
// Kaldi's default — a Hann window raised to 0.85 — and deliberately not what this package uses.
func poveyWindow(n int) []float32 {
	w := make([]float32, n)
	for i := range w {
		hann := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
		w[i] = float32(math.Pow(hann, 0.85))
	}
	return w
}
