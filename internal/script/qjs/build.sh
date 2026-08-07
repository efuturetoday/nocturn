#!/usr/bin/env bash
# build.sh — rebuild internal/script/qjs/nocturn-qjs.wasm from quickjs-ng + our
# C shim (nocturn-qjs.c). The resulting .wasm is committed and embedded
# (go:embed), so a normal `go build` needs none of this.
#
# CI runs this on every build and COMMITS the result when it differs (the qjs job
# in .github/workflows/ci.yml), so a shim change whose artefact was forgotten
# heals itself rather than shipping stale bytes. Running it by hand is therefore
# optional — it is just the faster way to see your change take effect, and it
# keeps the bot out of your history.
#
# Dev-tools (not runtime deps — the shipped binary is pure Go/wazero, no CGo):
#   - wasi-sdk  (clang targeting wasm32-wasi + wasi-libc sysroot)
#   - a quickjs-ng checkout (the interpreter sources)
#   - wabt's wasm2wat (optional, to inspect imports/exports)
#
# Usage:
#   WASI_SDK=/path/to/wasi-sdk QJS_NG=/path/to/quickjs-ng ./build.sh
# If unset, it fetches pinned versions into ./.buildcache.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
cache="$here/.buildcache"
WASI_SDK_VER="wasi-sdk-33"
QJS_NG_REF="v0.10.1"                                             # readable name; the SHA below is the pin
QJS_NG_SHA="3c9afc9943323ee9c7dbd123c0cd991448f4b6c2"            # what that tag pointed at when it was chosen

# Both downloads are verified, because CI rebuilds this artefact and commits the result: an
# unverified source would let a moved tag or a replaced tarball put bytes into main with nothing in
# between reading them. A tag is a name and names move; a commit SHA and a file digest do not.
# Upstream publishes these — `gh release view wasi-sdk-33 --json assets` — so bumping means
# replacing them, deliberately, in the same commit as the version.
wasi_sdk_sha() {
  case "$1" in
    wasi-sdk-33.0-arm64-macos.tar.gz)  echo 85c997a2665ead91673b5bb88b7d0df3fc8900df3bfa244f720d478187bbdc78 ;;
    wasi-sdk-33.0-x86_64-macos.tar.gz) echo 18f3f201ba9734e6a4455b0b6410690395a55e9ffa9f6f5066f66083a94b93b3 ;;
    wasi-sdk-33.0-x86_64-linux.tar.gz) echo 0ba8b5bfaeb2adf3f29bab5841d76cf5318ab8e1642ea195f88baba1abd47bce ;;
    wasi-sdk-33.0-arm64-linux.tar.gz)  echo 4f98ee738c7abb45c81a94d1461fc53cc569d1cd01498951c8184d841a027844 ;;
    *) echo "" ;;
  esac
}

# sha256 of $1, on whichever of the two tools this machine has.
sha256_of() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d" " -f1
  else shasum -a 256 "$1" | cut -d" " -f1
  fi
}

fetch_wasi_sdk() {
  local os arch asset
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"; [ "$os" = darwin ] && os=macos
  arch="$(uname -m)"; [ "$arch" = x86_64 ] && arch=x86_64
  asset="${WASI_SDK_VER}.0-${arch}-${os}.tar.gz"
  mkdir -p "$cache"
  dir="$cache/${WASI_SDK_VER}.0-${arch}-${os}"
  tarball="$cache/wasi-sdk.tar.gz"

  want="$(wasi_sdk_sha "$asset")"
  if [ -z "$want" ]; then
    echo "build.sh: no pinned digest for $asset — add one to wasi_sdk_sha before building here" >&2
    exit 1
  fi
  [ -f "$tarball" ] || curl -sL \
    "https://github.com/WebAssembly/wasi-sdk/releases/download/${WASI_SDK_VER}/${asset}" -o "$tarball"

  # Verified on EVERY run, not only on the run that downloads — the same shape fetch_qjs uses, where
  # the rev-parse sits outside its `if`. Both checks used to be guarded by "is it already here?",
  # which is exactly the question a cache answers with yes: CI restores this directory, so the digest
  # went unchecked on every run after the first, and the compiler that produces the committed
  # artefact was the one thing in the chain nobody looked at. A digest that only runs on a cold cache
  # verifies the case that was never in doubt.
  got="$(sha256_of "$tarball")"
  if [ "$got" != "$want" ]; then
    echo "build.sh: $asset does not match its pinned digest" >&2
    echo "  want $want" >&2
    echo "  got  $got" >&2
    rm -f "$tarball"
    exit 1
  fi

  # Extracted from the verified tarball every run rather than trusted from the cache. Keeping the
  # extracted tree would leave it verifiable only by association: someone who can write the cache
  # could leave the tarball honest and the toolchain beside it not. Ten seconds buys the link.
  rm -rf "$dir"
  tar xzf "$tarball" -C "$cache"
  echo "$dir"
}

fetch_qjs() {
  mkdir -p "$cache"
  if [ ! -d "$cache/quickjs-ng" ]; then
    # Fetch the commit itself rather than the tag, so a moved tag cannot change what is built.
    git -c init.defaultBranch=main init -q "$cache/quickjs-ng"
    git -C "$cache/quickjs-ng" remote add origin https://github.com/quickjs-ng/quickjs.git
    if ! git -C "$cache/quickjs-ng" fetch -q --depth 1 origin "$QJS_NG_SHA"; then
      rm -rf "$cache/quickjs-ng"
      echo "build.sh: cannot fetch quickjs-ng $QJS_NG_SHA ($QJS_NG_REF)" >&2
      exit 1
    fi
    git -C "$cache/quickjs-ng" checkout -q FETCH_HEAD
  fi
  got="$(git -C "$cache/quickjs-ng" rev-parse HEAD)"
  if [ "$got" != "$QJS_NG_SHA" ]; then
    echo "build.sh: quickjs-ng checkout is $got, pinned to $QJS_NG_SHA" >&2
    exit 1
  fi
  echo "$cache/quickjs-ng"
}

WASI_SDK="${WASI_SDK:-$(fetch_wasi_sdk)}"
QJS_NG="${QJS_NG:-$(fetch_qjs)}"
CLANG="$WASI_SDK/bin/clang"

# Core quickjs-ng sources + our WASI-command shim. This list is the `qjs_sources`
# set in the pinned checkout's CMakeLists.txt — check it against that file when
# bumping QJS_NG_REF rather than assuming, since the set moves between releases
# (v0.10.1 dropped dtoa.c and added xsum.c).
# One host import (nocturn.call), malloc/free exported for the packed-ptr ABI,
# a generous C stack for the recursive parser/GC.
"$CLANG" --target=wasm32-wasip1 -O2 -D_GNU_SOURCE \
  -I "$QJS_NG" \
  -Wl,-z,stack-size=4194304 \
  -Wl,--export=malloc -Wl,--export=free \
  "$QJS_NG/quickjs.c" "$QJS_NG/libregexp.c" "$QJS_NG/libunicode.c" \
  "$QJS_NG/cutils.c" "$QJS_NG/xsum.c" \
  "$here/nocturn-qjs.c" \
  -o "$here/nocturn-qjs.wasm"

echo "built $here/nocturn-qjs.wasm ($(wc -c < "$here/nocturn-qjs.wasm") bytes)"
command -v wasm2wat >/dev/null && wasm2wat "$here/nocturn-qjs.wasm" | grep -E '\(import|\(export "(malloc|free|memory|_start)"' || true
