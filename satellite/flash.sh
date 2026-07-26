#!/usr/bin/env bash
# Flash the satellite from the host, using artefacts built in the container.
#
# Why not flash from inside the container: OrbStack does forward the board into its Linux VM, and
# esptool there opens it and then dies with "stack smashing detected" — pyserial's native ioctls do
# not survive the forwarded device node. Reproducible, not a transient state. `orb usb attach` is not
# the fix either: it classifies the ESP32-S3's composite JTAG/CDC device as a network device, yields
# no ttyACM, and takes the working shared node away from macOS until the board is replugged.
#
# So: build in the container (fast, no toolchain on the host), flash on the host with Espressif's
# standalone esptool — a native binary, so this still pulls no Python onto the machine.
#
#   ./flash.sh              export artefacts and flash
#   ./flash.sh --monitor    flash, then tail the serial log until Ctrl-C
#   PORT=/dev/cu.usbserial-x ./flash.sh
set -euo pipefail

ESPTOOL_VERSION="${ESPTOOL_VERSION:-v5.3.1}"
BUILD_VOL="${BUILD_VOL:-nocturn-satellite-build}"
BAUD="${BAUD:-460800}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tools="$here/.tools"
staging="$here/.flash"

if [[ -z "${PORT:-}" ]]; then
	ports=()
	for p in /dev/cu.usbmodem* /dev/cu.usbserial* /dev/cu.wchusbserial*; do
		[[ -e $p ]] && ports+=("$p")
	done
	case ${#ports[@]} in
	0) echo "no board found — plug it in, or set PORT=" >&2; exit 1 ;;
	1) PORT="${ports[0]}" ;;
	*) echo "several candidates, set PORT= to one of: ${ports[*]}" >&2; exit 1 ;;
	esac
fi

# The standalone esptool, fetched once. Espressif ships it as a native binary precisely so a machine
# does not need a Python environment to talk to a board.
esptool="$tools/esptool-macos-arm64/esptool"
if [[ ! -x $esptool ]]; then
	echo "fetching esptool $ESPTOOL_VERSION"
	mkdir -p "$tools"
	curl -fsSL "https://github.com/espressif/esptool/releases/download/${ESPTOOL_VERSION}/esptool-${ESPTOOL_VERSION}-macos-arm64.tar.gz" |
		tar xz -C "$tools"
fi

# The build tree lives in a volume (see build.sh), so the images have to come out. It is a handful of
# files and a couple of megabytes — the flash args name exactly which, and at which offsets.
echo "exporting from $BUILD_VOL"
rm -rf "$staging"
mkdir -p "$staging"
docker run --rm -v "$BUILD_VOL":/build -v "$staging":/out alpine sh -c '
	cd /build || exit 1
	cp flash_args /out/
	for f in $(awk "{print \$2}" flash_args | grep "\.bin$"); do
		mkdir -p /out/$(dirname "$f")
		cp "$f" "/out/$f"
	done'

echo "flashing $PORT"
(cd "$staging" && "$esptool" --chip esp32s3 -p "$PORT" -b "$BAUD" \
	--before default-reset --after hard-reset write-flash "@flash_args")

if [[ "${1:-}" == "--monitor" ]]; then
	echo "--- monitor ($PORT), Ctrl-C to stop ---"
	# The chip's native USB CDC ignores the line rate; raw keeps the terminal from eating control
	# characters in the log.
	stty -f "$PORT" raw 115200 2>/dev/null || true
	exec cat "$PORT"
fi
