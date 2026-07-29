"""Emit the reference log-Mel features torchaudio produces, for internal/speaker/testdata.

This exists because the filterbank cannot be validated by reading the specification. WeSpeaker's
checkpoints were trained on features from one specific implementation, and a faithful-looking
reimplementation can differ in ways no plausibility test detects — Kaldi's documented default window
is "povey" while WeSpeaker passes window_type='hamming', and features built either way look equally
healthy. Only a comparison against the real implementation separates them. See ../fbank.go.

Settings are read from wespeaker/dataset/processor.py:

    mat = kaldi.fbank(waveform, num_mel_bins=80, frame_length=25, frame_shift=10,
                      dither=dither, sample_frequency=sample['sample_rate'],
                      window_type='hamming', use_energy=False)

with apply_cmvn's `mat - torch.mean(mat, dim=0)` afterwards, and `waveform * (1 << 15)` before —
Kaldi works on the integer sample scale.

Dither is 1.0 in training. It is zero here: it adds random noise by design, and a reference has to
be reproducible.

Usage:
    ./setup.sh                       # once, creates the venv
    .venv/bin/python fbank_golden.py ../testdata/kaldi_fbank.golden.json
"""

import json
import sys

import torch
import torchaudio.compliance.kaldi as kaldi

SAMPLES = 8000  # half a second at 16 kHz — 48 frames, enough to pin every stage


def lcg_pcm(n, seed=12345):
    """Generate the test signal.

    An integer linear congruential generator, so the Go side reproduces it bit for bit. A
    floating-point signal (a sine, say) would differ in its last bits between Python and Go, and
    that difference would surface in the comparison as if the filterbank were at fault.
    """
    x = seed
    out = []
    for _ in range(n):
        x = (1103515245 * x + 12345) & 0x7FFFFFFF
        out.append(((x >> 8) & 0xFFFF) - 32768)
    return out


def main():
    if len(sys.argv) != 2:
        sys.exit(f"usage: {sys.argv[0]} <output.json>")

    pcm = lcg_pcm(SAMPLES)
    # Already on the integer scale, so no <<15 here.
    waveform = torch.tensor([pcm], dtype=torch.float32)

    feat = kaldi.fbank(
        waveform,
        num_mel_bins=80,
        frame_length=25,
        frame_shift=10,
        dither=0.0,
        sample_frequency=16000,
        window_type="hamming",
        use_energy=False,
    )
    feat = feat - torch.mean(feat, dim=0)  # apply_cmvn, mean only, over time

    frames, bins = feat.shape
    out = {
        "reference": f"torchaudio {torch.__version__} compliance.kaldi.fbank, WeSpeaker settings",
        "settings": "num_mel_bins=80 frame_length=25 frame_shift=10 dither=0 "
                    "window_type=hamming use_energy=False, then mean normalisation over time",
        "pcm_generator": "x = (1103515245*x + 12345) mod 2^31, sample = ((x >> 8) & 0xFFFF) - 32768",
        "pcm_seed": 12345,
        "pcm_samples": SAMPLES,
        "frames": frames,
        "mel_bins": bins,
        "features": [round(v, 6) for v in feat.flatten().tolist()],
    }
    with open(sys.argv[1], "w") as f:
        json.dump(out, f, indent=1)
        f.write("\n")
    print(f"wrote {sys.argv[1]} — {frames} frames x {bins} bins")


if __name__ == "__main__":
    main()
