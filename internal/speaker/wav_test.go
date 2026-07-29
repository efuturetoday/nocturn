package speaker

import (
	"encoding/binary"
	"fmt"
	"os"
)

// readWAV decodes a 16-bit PCM mono WAV file at SampleRate.
//
// Deliberately strict: it refuses anything it would otherwise have to resample or downmix, because
// silently accepting 44.1 kHz stereo would produce features that are wrong in a way no assertion
// here would catch. Conversion is the caller's job, and `sox in.wav -r 16000 -c 1 -b 16 out.wav`
// does it.
//
// This lives in the test files because nothing in the package proper reads files — the daemon hands
// over PCM it already holds.
func readWAV(path string) ([]int16, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, fmt.Errorf("%s: not a RIFF/WAVE file", path)
	}

	var format, channels uint16
	var rate uint32
	var bits uint16
	var data []byte

	// Walk the chunk list rather than assuming fmt comes first and data second; encoders insert
	// LIST and fact chunks freely.
	for at := 12; at+8 <= len(raw); {
		id := string(raw[at : at+4])
		size := int(binary.LittleEndian.Uint32(raw[at+4 : at+8]))
		body := at + 8
		if body+size > len(raw) {
			size = len(raw) - body // tolerate a truncated final chunk
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, fmt.Errorf("%s: fmt chunk is %d bytes, want at least 16", path, size)
			}
			format = binary.LittleEndian.Uint16(raw[body : body+2])
			channels = binary.LittleEndian.Uint16(raw[body+2 : body+4])
			rate = binary.LittleEndian.Uint32(raw[body+4 : body+8])
			bits = binary.LittleEndian.Uint16(raw[body+14 : body+16])
		case "data":
			data = raw[body : body+size]
		}
		at = body + size
		if size%2 == 1 {
			at++ // chunks are word-aligned
		}
	}

	switch {
	case format != 1:
		return nil, fmt.Errorf("%s: format %d, want 1 (uncompressed PCM)", path, format)
	case channels != 1:
		return nil, fmt.Errorf("%s: %d channels, want mono", path, channels)
	case rate != SampleRate:
		return nil, fmt.Errorf("%s: %d Hz, want %d", path, rate, SampleRate)
	case bits != 16:
		return nil, fmt.Errorf("%s: %d bits per sample, want 16", path, bits)
	case data == nil:
		return nil, fmt.Errorf("%s: no data chunk", path)
	}

	pcm := make([]int16, len(data)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
	}
	return pcm, nil
}
