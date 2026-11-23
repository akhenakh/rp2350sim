#!/usr/bin/env bash
# Regenerate the guide screenshots that cover the newer boards and tabs.
# The UI renders at 2x for headless PNGs; downscale to the 1280x820 size used
# by docs/images. Usage: tools/render_docs_images.sh [build-dir]
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
BUILD=${1:-build}
UI="$ROOT/$BUILD/rp2350sim-ui"
IMAGES="$ROOT/docs/images"

if ! command -v ffmpeg >/dev/null 2>&1; then
	echo "render_docs_images: ffmpeg is required to downscale the UI renders" >&2
	exit 1
fi

cd "$ROOT/cmd/simui"
go build -o "$UI" .

render() {
	local board=$1 tab=$2 run=$3 name=$4
	shift 4
	local tmp="$ROOT/$BUILD/docs-$name-2x.png"
	"$UI" --board "$board" --tab "$tab" --width 1280 --height 820 \
		--run "$run" --png "$tmp" "$@" >/dev/null
	ffmpeg -v error -y -i "$tmp" -vf scale=1280:820:flags=lanczos \
		"$IMAGES/$name.png"
}

render pico2-dvi dvi 0.5 dvi "$ROOT/$BUILD/pico-examples/build/dvi_out_hstx_encoder/dvi_out_hstx_encoder.elf"
render pico2-midi midi 1 midi "$ROOT/$BUILD/midi.elf"
render pico2-audio audio 1 audio "$ROOT/$BUILD/audio.elf"
render pico2-analog scope 0.02 scope
render pico2-analog logic 0.05 logic "$ROOT/$BUILD/protocols.elf"
render pico2 board 0.3 board "$ROOT/$BUILD/blinky.elf"
render pico2-inputs board 0 inputs --input btn1=on --input sw1=on --input pot1=2.2 --input enc1=+2
render pico2b board 0.1 rp2350b
render pico2 console 1 console "$ROOT/firmware/c/build/usbhello.elf"

flash="${TMPDIR:-/tmp}/rp2350sim-docs-ota.bin"
rm -f "$flash"
"$UI" --board pico2 --flash "$flash" --run 0.5 --width 1280 --height 820 \
	--png "$ROOT/$BUILD/docs-ota-2x.png" "$ROOT/firmware/c/build/ota.elf" >/dev/null
ffmpeg -v error -y -i "$ROOT/$BUILD/docs-ota-2x.png" -vf scale=1280:820:flags=lanczos \
	"$IMAGES/ota.png"
rm -f "$flash"

echo "updated docs/images: dvi midi audio scope logic board inputs rp2350b console ota"
