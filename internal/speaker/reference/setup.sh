#!/usr/bin/env bash
# Create the Python environment that generates the filterbank reference.
#
# Python is a development tool here and never ships: it produces testdata/kaldi_fbank.golden.json
# once, that file is committed, and the binary knows nothing about any of it. The venv lives beside
# this script and is gitignored.
#
# torchaudio is the point. A second implementation written from the same reading of the
# specification would only confirm the reading — and the reading was wrong once already.
set -euo pipefail

cd "$(dirname "$0")"

python3 -m venv .venv
.venv/bin/pip install --quiet --disable-pip-version-check --upgrade pip
.venv/bin/pip install --quiet --disable-pip-version-check torch torchaudio

.venv/bin/python - <<'PY'
import torch, torchaudio
print(f"torch {torch.__version__} / torchaudio {torchaudio.__version__}")
PY

cat <<'EOF'

Ready. Regenerate the filterbank reference with:

    .venv/bin/python fbank_golden.py ../testdata/kaldi_fbank.golden.json

Only do that when the frontend specification itself changes. A golden file that stops matching is
the test working; refreshing it to make a failure disappear discards the only check that catches a
silently wrong frontend.
EOF
