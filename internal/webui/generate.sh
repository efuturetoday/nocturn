#!/usr/bin/env bash
#
# Copy the built Angular bundle into this package so //go:embed can reach it.
#
# It COPIES; it does not build. Two reasons, and both have teeth:
#
#   - `go generate ./...` must stay a fast, safe thing to run. A generator that silently starts a
#     multi-minute npm install is one nobody runs, and then the committed state drifts.
#   - In CI the bundle is not built here at all. .github/workflows/_build-web.yml builds it once,
#     tests it, and uploads it as an artefact precisely so the bytes that were tested are the bytes
#     that ship. Rebuilding here would quietly ship a second, untested build.
#
# A missing bundle is NOT an error: a binary without the web UI is a supported configuration (it
# serves a page saying how to build it), so this says what to run and exits 0.
#
#   ./generate.sh            copy whatever mobile/ has already built
#   ./generate.sh --build    run the Angular production build first
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$here/../.." && pwd)
mobile="$root/mobile"
# webDir in mobile/capacitor.config.ts — the same directory Capacitor packages for iOS and Android.
src="$mobile/dist/mobile/browser"
dest="$here/dist"

if [[ "${1:-}" == "--build" ]]; then
  echo "webui: building the Angular bundle in $mobile"
  (cd "$mobile" && npm ci && npm run build)
fi

if [[ ! -f "$src/index.html" ]]; then
  echo "webui: no bundle at $src — the binary will serve the 'not built' page."
  echo "webui: build it with:  cd mobile && npm ci && npm run build"
  echo "webui: or in one step: $0 --build"
  exit 0
fi

# Replace rather than merge. Angular hashes asset filenames, so a merge would accumulate every past
# build's chunks in the binary — megabytes of files nothing references and nothing ever removes.
# .gitkeep is what keeps the directory embeddable from a bare clone, so it is restored, not copied.
rm -rf "$dest"
mkdir -p "$dest"
touch "$dest/.gitkeep"
cp -R "$src"/. "$dest"/

echo "webui: copied $(find "$dest" -type f ! -name .gitkeep | wc -l | tr -d ' ') files into internal/webui/dist"
