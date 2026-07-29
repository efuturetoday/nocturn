package speaker

import (
	"math"
	"math/cmplx"
	"math/rand/v2"
	"testing"
)

// tone renders a sine of the given frequency at int16 scale.
func tone(hz float64, samples int, amplitude float64) []int16 {
	pcm := make([]int16, samples)
	for i := range pcm {
		pcm[i] = int16(amplitude * math.Sin(2*math.Pi*hz*float64(i)/SampleRate))
	}
	return pcm
}

func TestFFTMatchesDirectTransform(t *testing.T) {
	// The FFT is the one piece here with a closed-form answer to check against, so check it
	// against the definition rather than against itself.
	const n = 64
	f := &FilterBank{twiddle: twiddles(n)}

	r := rand.New(rand.NewPCG(7, 11))
	in := make([]complex128, n)
	for i := range in {
		in[i] = complex(r.NormFloat64(), 0)
	}
	got := append([]complex128(nil), in...)
	f.fft(got)

	for k := range n {
		var want complex128
		for j := range n {
			want += in[j] * cmplx.Exp(complex(0, -2*math.Pi*float64(k*j)/n))
		}
		if cmplx.Abs(got[k]-want) > 1e-9 {
			t.Fatalf("bin %d = %v, want %v", k, got[k], want)
		}
	}
}

func TestHammingWindowShape(t *testing.T) {
	w := hammingWindow(frameLength)
	if len(w) != frameLength {
		t.Fatalf("window has %d samples, want %d", len(w), frameLength)
	}
	// A symmetric Hamming window starts and ends at alpha-beta and peaks at alpha+beta.
	ends := float32(hammingAlpha - hammingBeta)
	if math.Abs(float64(w[0]-ends)) > 1e-6 || math.Abs(float64(w[len(w)-1]-ends)) > 1e-6 {
		t.Errorf("window ends at %v and %v, want %v at both ends", w[0], w[len(w)-1], ends)
	}
	if mid := w[(frameLength-1)/2]; math.Abs(float64(mid)-1) > 1e-3 {
		t.Errorf("window peaks at %v, want close to 1", mid)
	}
	// Pin the coefficients themselves, not just the shape: a Hann window would give 0.5 here, and
	// the families are close enough that only the values tell them apart.
	at := (frameLength - 1) / 4
	want := hammingAlpha - hammingBeta*math.Cos(2*math.Pi*float64(at)/float64(frameLength-1))
	if math.Abs(float64(w[at])-want) > 1e-6 {
		t.Errorf("window at sample %d is %v, want %v", at, w[at], want)
	}
}

func TestMelFiltersTileTheSpectrum(t *testing.T) {
	bank := melFilters()
	if len(bank) != MelBins {
		t.Fatalf("built %d filters, want %d", len(bank), MelBins)
	}

	// The triangles live in the Mel domain but are sampled at FFT bin frequencies. Below roughly
	// 500 Hz a triangle is narrower than the 31.25 Hz bin spacing, so it may cover a single bin,
	// peak well below its apex, and share that bin with its neighbours. That is Kaldi's actual
	// behaviour, so the assertions have to hold for the sparse low end as well as the dense top.
	prevPeak := -1
	for b, row := range bank {
		peak, peakAt, covered := float32(0), -1, 0
		for j, w := range row {
			if w < 0 || w > 1 {
				t.Fatalf("filter %d has weight %v at bin %d, want within [0,1]", b, w, j)
			}
			if w > 0 {
				covered++
			}
			if w > peak {
				peak, peakAt = w, j
			}
		}
		if covered == 0 {
			t.Errorf("filter %d covers no FFT bin at all", b)
			continue
		}
		// Once a triangle spans several bins, one of them must land near its apex.
		if covered >= 3 && peak < 0.5 {
			t.Errorf("filter %d spans %d bins but peaks at only %v", b, covered, peak)
		}
		if peakAt < prevPeak {
			t.Errorf("filter %d peaks at bin %d, below the previous filter's %d", b, peakAt, prevPeak)
		}
		prevPeak = peakAt
	}

	// Mel spacing is near-linear at low frequencies and logarithmic above: the last filter must
	// therefore span far more FFT bins than the first.
	width := func(row []float32) int {
		n := 0
		for _, w := range row {
			if w > 0 {
				n++
			}
		}
		return n
	}
	if first, last := width(bank[0]), width(bank[MelBins-1]); last <= first*3 {
		t.Errorf("lowest filter spans %d bins and highest spans %d; expected the highest to be far wider",
			first, last)
	}
}

func TestFrameCountFollowsSnipEdges(t *testing.T) {
	fb := NewFilterBank()
	for _, tc := range []struct{ samples, want int }{
		{frameLength, 1},                  // exactly one window fits
		{frameLength + frameShift - 1, 1}, // not quite enough for a second
		{frameLength + frameShift, 2},
		{SampleRate, 1 + (SampleRate-frameLength)/frameShift}, // one second
	} {
		f, err := fb.Compute(tone(440, tc.samples, 8000))
		if err != nil {
			t.Fatalf("%d samples: %v", tc.samples, err)
		}
		if f.Frames != tc.want {
			t.Errorf("%d samples produced %d frames, want %d", tc.samples, f.Frames, tc.want)
		}
		if len(f.Data) != f.Frames*MelBins {
			t.Errorf("%d samples: data holds %d values, want %d", tc.samples, len(f.Data), f.Frames*MelBins)
		}
	}
}

func TestTooShortIsRejected(t *testing.T) {
	if _, err := NewFilterBank().Compute(tone(440, frameLength-1, 8000)); err == nil {
		t.Fatal("a signal shorter than one window returned no error")
	}
}

func TestToneLandsInTheExpectedMelBin(t *testing.T) {
	// A pure tone must excite the filters covering its frequency and little else. This is the
	// test that catches a Mel scale, an FFT bin width or a sample rate being wrong — the failures
	// that otherwise stay invisible because the output still looks like plausible features.
	fb := NewFilterBank()
	for _, hz := range []float64{300, 1000, 3000} {
		// The unnormalised path: a steady tone looks identical in every frame, so mean
		// normalisation would subtract precisely the energy this test is looking for.
		f, err := fb.compute(tone(hz, SampleRate/2, 8000))
		if err != nil {
			t.Fatal(err)
		}
		// Find which filter weights this tone's frequency most heavily.
		wantBin, wantWeight := -1, float32(0)
		binIndex := int(hz / (float64(SampleRate) / fftSize))
		for b, row := range fb.melBank {
			if row[binIndex] > wantWeight {
				wantBin, wantWeight = b, row[binIndex]
			}
		}

		// In the middle frame, the loudest coefficient should be at or beside that filter.
		row := f.At(f.Frames / 2)
		gotBin, gotValue := 0, row[0]
		for b, v := range row {
			if v > gotValue {
				gotBin, gotValue = b, v
			}
		}
		if diff := gotBin - wantBin; diff < -1 || diff > 1 {
			t.Errorf("%.0f Hz peaks at Mel bin %d, expected bin %d", hz, gotBin, wantBin)
		}
	}
}

func TestMeanNormalisationCentresEachCoefficient(t *testing.T) {
	// After normalisation every coefficient averages to zero over the utterance. This is what
	// makes a constant channel gain drop out, so it is worth pinning rather than assuming.
	f, err := NewFilterBank().Compute(tone(440, SampleRate, 8000))
	if err != nil {
		t.Fatal(err)
	}
	for b := range MelBins {
		var sum float64
		for i := range f.Frames {
			sum += float64(f.At(i)[b])
		}
		if mean := sum / float64(f.Frames); math.Abs(mean) > 1e-3 {
			t.Errorf("coefficient %d averages %v over the utterance, want zero", b, mean)
		}
	}
}

func TestGainIsAbsorbedByNormalisation(t *testing.T) {
	// The same utterance recorded louder must yield near-identical features: scaling the signal
	// shifts every log-Mel value by a constant, which mean normalisation subtracts away. This is
	// the property that lets one enrolled profile survive a change in recording level.
	fb := NewFilterBank()
	quiet, err := fb.Compute(tone(700, SampleRate, 2000))
	if err != nil {
		t.Fatal(err)
	}
	loud, err := fb.Compute(tone(700, SampleRate, 16000))
	if err != nil {
		t.Fatal(err)
	}
	if quiet.Frames != loud.Frames {
		t.Fatalf("frame counts differ: %d and %d", quiet.Frames, loud.Frames)
	}
	var worst float64
	for i := range quiet.Data {
		worst = math.Max(worst, math.Abs(float64(quiet.Data[i]-loud.Data[i])))
	}
	if worst > 0.05 {
		t.Errorf("an eightfold gain change moved a coefficient by %v, want it absorbed", worst)
	}
}

func TestSilenceProducesFiniteFeatures(t *testing.T) {
	// Digital silence drives every filter energy to zero; without the log floor this would be
	// negative infinity and would poison the whole utterance through mean normalisation.
	f, err := NewFilterBank().Compute(make([]int16, SampleRate))
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range f.Data {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			t.Fatalf("silence produced %v at index %d", v, i)
		}
	}
}
