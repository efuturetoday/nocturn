#!/usr/bin/env bash
# Write this board's identity into its NVS partition.
#
# A satellite cannot obtain either half of what it needs. It has no screen to show a pairing code and
# no keyboard to enter one, so the join flow has nothing to work with — and a device that could
# enrol itself would not be authorised by anyone. An already-paired device asks the daemon for a
# bearer on its behalf, and this puts that bearer, plus the network to reach it on, onto the board.
#
# Today the handover is a flash. Later it will be a phone over BLE; only where the values come from
# changes, not what the firmware reads.
#
#   ./provision.sh --ssid home --pass secret --bearer TOKEN
#   ./provision.sh ... --host 192.168.2.179 --port 8765   # when mDNS does not get through
#
# Get the bearer from an already-paired device:
#
#   curl -sX POST http://<daemon>:8080/devices \
#        -H "Authorization: Bearer <that device's token>" \
#        -d '{"name":"hallway","class":"appliance"}'
#
# The daemon shows it once — afterwards only its hash exists, so a lost token means enrolling again
# rather than recovering it.
set -euo pipefail

IDF_IMAGE="${IDF_IMAGE:-espressif/idf:v5.5}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

ssid="" pass="" bearer="" host="" port=""
while [[ $# -gt 0 ]]; do
	case $1 in
	--ssid) ssid=$2; shift 2 ;;
	--pass) pass=$2; shift 2 ;;
	--bearer) bearer=$2; shift 2 ;;
	--host) host=$2; shift 2 ;;
	--port) port=$2; shift 2 ;;
	*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
done
if [[ -z $ssid || -z $pass || -z $bearer ]]; then
	echo "usage: $0 --ssid NAME --pass SECRET --bearer TOKEN" >&2
	exit 2
fi

# The partition table's nvs entry: 24 KB at 0x9000. Read from partitions.csv rather than repeated
# here, so the two cannot drift.
offset=$(awk -F',' '$1 ~ /^nvs/ { gsub(/ /,"",$4); print $4 }' "$here/partitions.csv")
size=$(awk -F',' '$1 ~ /^nvs/ { gsub(/ /,"",$5); print $5 }' "$here/partitions.csv")
: "${offset:?no nvs partition in partitions.csv}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The CSV holds the token in the clear, which is why it lives in a temporary directory that is
# removed on exit rather than anywhere in the tree.
cat >"$work/nvs.csv" <<CSV
key,type,encoding,value
nocturn,namespace,,
ssid,data,string,$ssid
pass,data,string,$pass
bearer,data,string,$bearer
CSV

# Optional: state the daemon's address when discovery cannot be relied on. Multicast is the least
# dependable thing on a home network, and a satellite that cannot find its daemon does nothing.
if [[ -n $host ]]; then
	printf 'host,data,string,%s\n' "$host" >>"$work/nvs.csv"
	printf 'port,data,u16,%s\n' "${port:-8765}" >>"$work/nvs.csv"
fi

echo "building nvs image ($size at $offset)"
docker run --rm ${DOCKER_TTY:--i} \
	-u "$(id -u):$(id -g)" -e HOME=/tmp \
	-v "$work":/work -w /work \
	"$IDF_IMAGE" \
	python /opt/esp/idf/components/nvs_flash/nvs_partition_generator/nvs_partition_gen.py \
	generate nvs.csv nvs.bin "$size"

port="${PORT:-$(ls /dev/cu.usbmodem* 2>/dev/null | head -1)}"
: "${port:?no board found — plug it in, or set PORT=}"

esptool="$here/.tools/esptool-macos-arm64/esptool"
if [[ ! -x $esptool ]]; then
	echo "run ./flash.sh once first — it fetches esptool" >&2
	exit 1
fi

echo "writing to $port at $offset"
"$esptool" --chip esp32s3 -p "$port" write-flash "$offset" "$work/nvs.bin"
echo "provisioned. the token is not stored anywhere on this machine."
