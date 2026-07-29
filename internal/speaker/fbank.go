package speaker

import (
	"fmt"
	"math"
)

// This file reproduces Kaldi's log-Mel filterbank exactly as torchaudio.compliance.kaldi.fbank
// implements it, because that is the frontend WeSpeaker and 3D-Speaker checkpoints were trained
// with. Every constant below is a training-time decision, not a tuning knob: a filterbank that is
// merely reasonable produces embeddings that look entirely plausible and compare meaninglessly,
// which is the single most common way a speaker-recognition port fails. Changing anything here
// invalidates every enrolled profile.
//
// The settings are torchaudio's defaults with WeSpeaker's overrides:
//
//	num_mel_bins 80 · frame_length 25 ms · frame_shift 10 ms · sample_frequency 16000
//	window HAMMING · preemphasis 0.97 · remove_dc_offset true · snip_edges true
//	round_to_power_of_two true · use_power true · use_log_fbank true · use_energy false
//	low_freq 20 Hz · high_freq = Nyquist · dither 0 (1.0 in training, but that is noise by design)
//
// followed by mean normalisation over the utterance, which WeSpeaker applies separately in
// apply_cmvn as `mat - mean(mat, dim=0)` — mean only, no variance scaling.
//
// The window is the trap: Kaldi's own default is "povey" and every description of Kaldi features
// says so, but WeSpeaker passes window_type='hamming' explicitly. Features built the documented way
// pass every plausibility check and compare worse than they should, so TestMatchesTorchaudio pins
// this against the real implementation rather than against a reading of it.

const (
	// SampleRate is the only rate these checkpoints accept, and the rate the satellite sends.
	SampleRate = 16000

	// MelBins is the feature dimension the networks expect.
	MelBins = 80

	frameLength = 25 * SampleRate / 1000 // 400 samples
	frameShift  = 10 * SampleRate / 1000 // 160 samples
	fftSize     = 512                    // frameLength rounded up to a power of two

	lowFreq     = 20.0
	highFreq    = SampleRate / 2.0
	preemphasis = 0.97

	// Kaldi's Hamming coefficients. Not the 0.54/0.46 rounding some references use for the
	// "raised cosine" family — these are the values torchaudio passes to torch.hamming_window.
	hammingAlpha = 0.54
	hammingBeta  = 0.46

	logFloor       = 1.1920929e-07 // float32 epsilon, the floor torchaudio applies before the log
	melScaleFactor = 1127.0
)

// Features is a filterbank result: Frames rows of MelBins values, stored row-major.
type Features struct {
	Frames int
	Data   []float32
}

// At returns the row for one frame.
func (f *Features) At(frame int) []float32 {
	return f.Data[frame*MelBins : (frame+1)*MelBins]
}

// FilterBank turns 16 kHz PCM into log-Mel features. It holds only precomputed tables, so one
// instance is safe to share across goroutines.
type FilterBank struct {
	window  []float32   // povey window, frameLength long
	melBank [][]float32 // MelBins × (fftSize/2), the triangular weights
	twiddle []complex128
}

// NewFilterBank precomputes the window, the Mel triangles and the FFT twiddle factors.
func NewFilterBank() *FilterBank {
	f := &FilterBank{
		window:  hammingWindow(frameLength),
		melBank: melFilters(),
		twiddle: twiddles(fftSize),
	}
	return f
}

// Compute turns PCM into features.
//
// The samples must be raw int16 values — Kaldi works on the integer scale, and WeSpeaker multiplies
// its normalised waveform back by 32768 before calling the filterbank. Passing samples normalised to
// ±1 shifts every log-Mel value by a constant ln(32768), which cepstral mean normalisation then
// removes almost entirely; the result looks healthy and is subtly wrong. Taking []int16 makes the
// mistake unrepresentable.
func (f *FilterBank) Compute(pcm []int16) (*Features, error) {
	out, err := f.compute(pcm)
	if err != nil {
		return nil, err
	}
	normalizeMean(out)
	return out, nil
}

// compute is Compute without the mean normalisation. Kept separate because normalisation is what
// makes a stationary signal's features vanish, which is exactly the wrong thing when the question
// is whether energy landed in the right Mel bin.
func (f *FilterBank) compute(pcm []int16) (*Features, error) {
	if len(pcm) < frameLength {
		return nil, fmt.Errorf("speaker: need at least %d samples (%d ms), got %d",
			frameLength, frameLength*1000/SampleRate, len(pcm))
	}
	// snip_edges: only whole windows that fit inside the signal produce a frame.
	frames := 1 + (len(pcm)-frameLength)/frameShift

	out := &Features{Frames: frames, Data: make([]float32, frames*MelBins)}
	buf := make([]float64, frameLength)
	spectrum := make([]float64, fftSize/2)
	re := make([]complex128, fftSize)

	for i := range frames {
		window := pcm[i*frameShift:]

		// Remove the DC offset first: Kaldi centres the raw frame before anything else.
		var mean float64
		for j := range frameLength {
			mean += float64(window[j])
		}
		mean /= frameLength
		for j := range frameLength {
			buf[j] = float64(window[j]) - mean
		}

		// Pre-emphasis, with the first sample using itself as its predecessor (replicate padding).
		prev := buf[0]
		for j := range frameLength {
			cur := buf[j]
			buf[j] = cur - preemphasis*prev
			prev = cur
		}

		for j := range frameLength {
			re[j] = complex(buf[j]*float64(f.window[j]), 0)
		}
		clear(re[frameLength:])
		f.fft(re)

		// Power spectrum. The Nyquist bin is omitted because every Mel triangle weights it zero.
		for j := range spectrum {
			c := re[j]
			spectrum[j] = real(c)*real(c) + imag(c)*imag(c)
		}

		row := out.At(i)
		for b, weights := range f.melBank {
			var energy float64
			for j, w := range weights {
				if w != 0 {
					energy += float64(w) * spectrum[j]
				}
			}
			row[b] = float32(math.Log(math.Max(energy, logFloor)))
		}
	}
	return out, nil
}

// normalizeMean subtracts each coefficient's mean over the utterance. This is the normalisation
// WeSpeaker applies before the network, and it is what makes the features insensitive to a constant
// channel gain — the reason a phone and a far-field microphone compare at all.
func normalizeMean(f *Features) {
	means := make([]float64, MelBins)
	for i := range f.Frames {
		for b, v := range f.At(i) {
			means[b] += float64(v)
		}
	}
	for b := range means {
		means[b] /= float64(f.Frames)
	}
	for i := range f.Frames {
		row := f.At(i)
		for b := range row {
			row[b] -= float32(means[b])
		}
	}
}

// hammingWindow is the symmetric (non-periodic) Hamming window: the denominator is n-1, so the
// window is zero-symmetric about its centre and both ends carry the same value.
func hammingWindow(n int) []float32 {
	w := make([]float32, n)
	for i := range w {
		w[i] = float32(hammingAlpha - hammingBeta*math.Cos(2*math.Pi*float64(i)/float64(n-1)))
	}
	return w
}

func mel(hz float64) float64 { return melScaleFactor * math.Log(1+hz/700) }

// melFilters builds the triangular filters in the Mel domain. Triangles are equally spaced in Mel
// and overlap by half, so each filter's peak sits on its neighbours' edges.
func melFilters() [][]float32 {
	const bins = fftSize / 2
	binWidth := float64(SampleRate) / fftSize

	melLow, melHigh := mel(lowFreq), mel(highFreq)
	delta := (melHigh - melLow) / float64(MelBins+1)

	bank := make([][]float32, MelBins)
	for b := range bank {
		left := melLow + float64(b)*delta
		center := left + delta
		right := left + 2*delta

		row := make([]float32, bins)
		for j := range row {
			m := mel(binWidth * float64(j))
			up := (m - left) / (center - left)
			down := (right - m) / (right - center)
			if w := math.Min(up, down); w > 0 {
				row[j] = float32(w)
			}
		}
		bank[b] = row
	}
	return bank
}

// --- FFT --------------------------------------------------------------------------------------

// twiddles precomputes the roots of unity for an n-point transform.
func twiddles(n int) []complex128 {
	w := make([]complex128, n/2)
	for i := range w {
		angle := -2 * math.Pi * float64(i) / float64(n)
		w[i] = complex(math.Cos(angle), math.Sin(angle))
	}
	return w
}

// fft transforms x in place. The length is always fftSize, a power of two, so the plain iterative
// radix-2 decimation-in-time algorithm applies without special cases.
func (f *FilterBank) fft(x []complex128) {
	n := len(x)

	// Bit-reversal permutation.
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j |= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}

	for length := 2; length <= n; length <<= 1 {
		step := n / length
		for i := 0; i < n; i += length {
			for j := range length / 2 {
				u := x[i+j]
				v := x[i+j+length/2] * f.twiddle[j*step]
				x[i+j] = u + v
				x[i+j+length/2] = u - v
			}
		}
	}
}
