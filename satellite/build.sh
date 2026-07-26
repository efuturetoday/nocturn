#!/usr/bin/env bash
# Build the satellite firmware in Espressif's toolchain container.
#
# The toolchain is not installed on the host on purpose: ESP-IDF wants its own Python virtualenv and
# ~2 GB of cross-compiler, and neither belongs on a machine whose Python is centrally managed. The
# image carries all of it, and there is a native arm64 build, so nothing here is emulated.
#
# Only the SOURCE is bind-mounted. The build tree and the component cache live in named volumes on
# the VM's own filesystem, because a bind mount is the wrong place for either: an ESP-IDF build
# writes tens of thousands of small object files, and doing that across the host boundary is where
# the time goes. You edit on the host, the compiler works on native storage.
#
#   ./build.sh              incremental build
#   ./build.sh fullclean    throw away the build tree
#   ./build.sh menuconfig   interactive config (writes sdkconfig, which IS on the host)
#   ./build.sh size         where the flash went
#
# Flashing is flash.sh — it needs the serial device and therefore runs differently.
set -euo pipefail

IDF_IMAGE="${IDF_IMAGE:-espressif/idf:v5.5}"
BUILD_VOL="${BUILD_VOL:-nocturn-satellite-build}"
CACHE_VOL="${CACHE_VOL:-nocturn-satellite-cache}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

docker volume create "$BUILD_VOL" >/dev/null
docker volume create "$CACHE_VOL" >/dev/null

# HOME lands in the cache volume: the component manager keeps its registry downloads there, and
# re-fetching esp-sr's models on every build would dwarf the compile itself.
exec docker run --rm ${DOCKER_TTY:--it} \
	-e HOME=/cache \
	-e IDF_CCACHE_ENABLE=1 \
	-v "$here":/project \
	-v "$BUILD_VOL":/build \
	-v "$CACHE_VOL":/cache \
	-w /project \
	"$IDF_IMAGE" \
	idf.py -B /build "${@:-build}"
