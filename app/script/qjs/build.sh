#!/usr/bin/env bash
# build.sh — rebuild app/script/qjs/nocturn-qjs.wasm from quickjs-ng + our
# C shim (nocturn-qjs.c). Run only when the shim changes; the resulting .wasm is
# committed and embedded (go:embed), so a normal `go build` needs none of this.
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
QJS_NG_REF="v0.10.1" # pin; bump deliberately

fetch_wasi_sdk() {
  local os arch asset
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"; [ "$os" = darwin ] && os=macos
  arch="$(uname -m)"; [ "$arch" = x86_64 ] && arch=x86_64
  asset="${WASI_SDK_VER}.0-${arch}-${os}.tar.gz"
  mkdir -p "$cache"
  if [ ! -d "$cache/${WASI_SDK_VER}.0-${arch}-${os}" ]; then
    curl -sL "https://github.com/WebAssembly/wasi-sdk/releases/download/${WASI_SDK_VER}/${asset}" \
      -o "$cache/wasi-sdk.tar.gz"
    tar xzf "$cache/wasi-sdk.tar.gz" -C "$cache"
  fi
  echo "$cache/${WASI_SDK_VER}.0-${arch}-${os}"
}

fetch_qjs() {
  mkdir -p "$cache"
  if [ ! -d "$cache/quickjs-ng" ]; then
    git clone --depth 1 --branch "$QJS_NG_REF" https://github.com/quickjs-ng/quickjs.git "$cache/quickjs-ng"
  fi
  echo "$cache/quickjs-ng"
}

WASI_SDK="${WASI_SDK:-$(fetch_wasi_sdk)}"
QJS_NG="${QJS_NG:-$(fetch_qjs)}"
CLANG="$WASI_SDK/bin/clang"

# Core quickjs-ng sources (the CMake qjs_sources set) + our WASI-command shim.
# One host import (nocturn.call), malloc/free exported for the packed-ptr ABI,
# a generous C stack for the recursive parser/GC.
"$CLANG" --target=wasm32-wasip1 -O2 -D_GNU_SOURCE \
  -I "$QJS_NG" \
  -Wl,-z,stack-size=4194304 \
  -Wl,--export=malloc -Wl,--export=free \
  "$QJS_NG/quickjs.c" "$QJS_NG/libregexp.c" "$QJS_NG/libunicode.c" "$QJS_NG/dtoa.c" \
  "$here/nocturn-qjs.c" \
  -o "$here/nocturn-qjs.wasm"

echo "built $here/nocturn-qjs.wasm ($(wc -c < "$here/nocturn-qjs.wasm") bytes)"
command -v wasm2wat >/dev/null && wasm2wat "$here/nocturn-qjs.wasm" | grep -E '\(import|\(export "(malloc|free|memory|_start)"' || true
