# internal/speaker testdata

## `kaldi_fbank.golden.json`

48 frames × 80 log-Mel coefficients that **torchaudio** produces for a fixed signal.
`TestMatchesTorchaudio` recomputes them with this package and requires agreement to 1e-3.

This file is the most valuable thing in the package, because the filterbank is the one part that
cannot be validated by reasoning about it. WeSpeaker's checkpoints were trained on features from one
specific implementation with one specific set of settings. Features that are merely *reasonable*
produce embeddings that look healthy, normalise correctly, put a tone in the right Mel bin — and
compare worse than they should, for no visible reason.

The window shows how narrow the margin for error is. Kaldi's documented default is `povey`;
WeSpeaker passes `window_type='hamming'`. Both produce features that satisfy every property test in
this package — right shape, right Mel bin, gain absorbed — but building it the documented way costs
roughly a fivefold reduction in separation margin, with nothing anywhere reporting a problem.

| | |
|---|---|
| Reference | `torchaudio 2.13.0`, `compliance.kaldi.fbank` |
| Settings | `num_mel_bins=80 frame_length=25 frame_shift=10 dither=0 window_type=hamming use_energy=False`, then mean normalisation over time |
| Signal | integer LCG, seed 12345, 8000 samples — reproduced bit for bit in Go |

The signal is an integer generator rather than a sine so both languages produce identical input; a
float signal would differ in its last bits and muddy the comparison.

`TestMatchesTorchaudio` also recomputes with the *wrong* window and requires that comparison to
fail. A golden test that cannot reject anything is decoration — the subtest reports the margin
(Povey shifts coefficients by up to 2.83 against a 1e-3 tolerance).

### Regenerating

```bash
cd internal/speaker/reference
./setup.sh                                                   # once
.venv/bin/python fbank_golden.py ../testdata/kaldi_fbank.golden.json
```

Only when the frontend specification changes. If this package stops matching a golden file nobody
regenerated, the test is working — do not refresh the file to make it pass.

## What is not committed

**The model.** 25–40 MB. Point `NOCTURN_SPEAKER_MODEL` at a WeSpeaker ResNet34 checkpoint; tests
that need it skip when it is unset. See `../../onnx/testdata/README.md` for the download.

**The evaluation corpus.** Built by `../reference/corpus.sh`, ~66 MB converted. It is input to a
measurement, not a fixture, so it is rebuilt rather than stored.

## Measuring

```bash
internal/speaker/reference/corpus.sh /tmp/corpus
NOCTURN_SPEAKER_MODEL=… NOCTURN_SPEAKER_CORPUS=/tmp/corpus \
  go test ./internal/speaker/ -run Evaluate -v -timeout 30m
```

`NOCTURN_SPEAKER_FOCUS=<speaker>` additionally reports that one speaker on their own. The aggregate
is dominated by whoever is most numerous in the corpus, so one voice recorded through a different
microphone disappears into it — adding sixteen satellite recordings to forty LibriSpeech speakers
moved the equal error rate from 0.83 % to 0.81 %, which says nothing about that microphone.

There are two different questions here and they have very different answers. Both are measured,
because reading only one of them leads to a wrong decision:

| | Question | Measured by | What it decides |
|---|---|---|---|
| **Verification** | Is this Oliver, against anyone at all? | `TestEvaluateCorpus` | whether the pipeline is correct; what identity would be worth if anything hung on it |
| **Identification** | Which of these four people just spoke? | `TestEvaluateHousehold` | the threshold that actually ships |

### Verification — open set, against strangers

LibriSpeech dev-clean, 40 speakers, 240 recordings, 600 genuine and 28 080 impostor pairs:

| | |
|---|---|
| Genuine pairs | 0.8186 ± 0.1044 |
| Impostor pairs | 0.1116 ± 0.1052 |
| Equal error rate | **0.83 %** at threshold 0.3983 |

0.83 % is what this checkpoint reports in its own published evaluation, which is the strongest
available evidence that the whole chain — filterbank, ONNX executor, GEMM kernels — is right.
Anything subtly wrong anywhere would show up as several percent. It is also the number to quote if
identity is ever proposed as a security control — see "Why this is not an authentication factor"
below, because the number is not the reason it fails.

### Identification — closed set, inside a household

`TestEvaluateHousehold` draws random households from the same corpus, enrols three recordings per
person and holds the rest back as queries. Beating three alternatives is a far easier problem than
beating everyone alive.

| Household size | Top-1, averaged profile | Top-1, best of takes | Queries |
|---|---|---|---|
| 2 | 100.00 % | 100.00 % | 1 200 |
| 3 | 100.00 % | 100.00 % | 1 800 |
| 4 | 100.00 % | 100.00 % | 2 400 |
| 5 | 100.00 % | 100.00 % | 3 000 |
| 6 | 100.00 % | 100.00 % | 3 600 |

Twelve thousand queries, not one mistake. Identification inside a household is simply not the hard
part — which means the threshold's only job is noticing someone who is *not* a member.

### Where the threshold belongs

Best match against a household of four:

| | Median | Tail |
|---|---|---|
| Member | 0.8750 | 0.6843 at the 5th percentile |
| Visitor | 0.2496 | 0.4218 at the 95th percentile |

| Reject threshold | Members turned away | Visitors taken for a member |
|---|---|---|
| 0.30 | 0.00 % | 31.67 % |
| 0.40 | 0.00 % | 6.83 % |
| **0.50** | **0.00 %** | **1.33 %** |

0.50 is the comfortable choice: no member was turned away in 2 400 queries, and one visitor in
seventy-five is mistaken for someone in the household. Since being wrong costs a misaddressed
sentence, the earlier 0.60 — chosen when this was framed as verification — was too cautious.

### Averaged profile versus keeping every take

They tie here, and that result should not be over-read. LibriSpeech is one microphone and one
session per speaker, so it cannot exercise the case the multi-vector shape was proposed for: the
same person enrolled on a phone and recognised through a far-field array with echo cancellation. An
averaged profile lands between two channels and matches neither. That question stays open until
recordings from the satellite exist, which is why profiles keep every take tagged by device rather
than collapsing to a centroid.

## A second channel: the satellite

Everything above is close-talking read English. The satellite is a different channel — microphone
array, beamformer, echo canceller, and the speaker several metres away. Sixteen recordings of one
voice through it, against the forty LibriSpeech speakers as impostors
(`NOCTURN_SPEAKER_FOCUS=oliver-satellite`):

| | Close-talking | Satellite |
|---|---|---|
| Same person | 0.8137 | **0.6302 ± 0.0609** |
| Against strangers | 0.1082 | **0.0425 ± 0.0890** |
| Worst own pair | 0.2916 | 0.4873 |
| Best impostor pair | 0.5806 | 0.3922 |

Separation is clean — no overlap, margin 0.0952 — but **the threshold has to move**. The 0.50 that
the close-talking corpus supports would reject that voice's own worst recording at 0.4873. For this
channel the operating point is nearer 0.45.

Three things keep the number provisional. The recordings run 1.4 to 5.2 seconds, and short
utterances embed more noisily, so part of the drop from 0.81 to 0.63 is duration rather than
channel. The impostors read English while the enrolled voice speaks German, which separates more
easily than a housemate would. And it is one speaker: this shows that voice separating from
strangers over this channel, not two people in a household separating from each other.

## Why this is not an authentication factor

Worth stating plainly, because 100 % top-1 and a 0.83 % equal error rate invite the conclusion that
identity could decide what is allowed. It cannot, and accuracy is not what stops it.

The threat is not a stranger who happens to sound similar — that is what the numbers above cover,
and they cover it well. The threat is someone who can play audio at the microphone. Cloning a voice
takes seconds of sample, and a recording is indistinguishable from presence to any embedding,
including a perfect one. Replay and synthesis detectors, cohort score normalisation and challenge
phrases all raise the cost; none change the shape, because the attacker owns the channel.

This is the same argument that puts approval out of band in the first place. In-band approval shares
a trust domain with the injection it is meant to catch, which is why a privileged action is
confirmed on a second device — and a voice arriving over that same in-band channel cannot be what
skips it. So identity selects context: which notes are folded into a prompt, how an answer is
addressed. It never converts a gate's "ask" into an "allow".

Nothing here is a constraint in practice. Differentiation was the whole requirement; the boundary is
recorded so that a later reader, looking at these accuracy figures, does not quietly cross it.
