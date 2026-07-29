#!/usr/bin/env bash
# Build an evaluation corpus from LibriSpeech dev-clean, for TestEvaluateCorpus.
#
# A decision threshold cannot be derived, only measured, and measuring it needs many speakers: with
# two people you get one genuine pair and one impostor pair, which is an anecdote. dev-clean gives
# 40 speakers under a permissive licence with no registration.
#
# Own recordings belong in the same tree, as one more speaker directory. That is the useful shape:
# five takes of one voice yield ten genuine pairs and over a thousand impostor pairs against the
# corpus, which is enough for a personal threshold without asking anyone else for their voice.
#
#   rec -r 16000 -c 1 -b 16 "$CORPUS/oliver/take-0.wav" trim 0 10
#
# LibriSpeech is CC BY 4.0 (Panayotov et al., ICASSP 2015). The corpus is not committed — it is
# ~66 MB converted, and it is input to a measurement, not a fixture.
set -euo pipefail

CORPUS="${1:-}"
if [[ -z "$CORPUS" ]]; then
    echo "usage: $0 <corpus-directory> [takes-per-speaker]" >&2
    exit 64
fi
TAKES="${2:-6}"

command -v ffmpeg >/dev/null || { echo "ffmpeg is required (brew install ffmpeg)" >&2; exit 69; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "downloading LibriSpeech dev-clean (337 MB)…"
curl -fL --progress-bar -o "$WORK/dev-clean.tar.gz" \
    https://www.openslr.org/resources/12/dev-clean.tar.gz
tar xzf "$WORK/dev-clean.tar.gz" -C "$WORK"

# The recordings are already 16 kHz mono FLAC; the conversion is a decode, not a resample. Files are
# taken in sorted order so two runs over one corpus compare the same recordings.
mkdir -p "$CORPUS"
for speaker in "$WORK"/LibriSpeech/dev-clean/*/; do
    id="$(basename "$speaker")"
    mkdir -p "$CORPUS/$id"
    n=0
    while IFS= read -r flac; do
        [[ $n -ge $TAKES ]] && break
        ffmpeg -loglevel error -y -i "$flac" -ar 16000 -ac 1 -c:a pcm_s16le \
            "$CORPUS/$id/take-$n.wav" </dev/null
        n=$((n + 1))
    done < <(find "$speaker" -name '*.flac' | sort)
done

echo "corpus ready: $(ls "$CORPUS" | wc -l | tr -d ' ') speakers, \
$(find "$CORPUS" -name '*.wav' | wc -l | tr -d ' ') recordings in $CORPUS"
