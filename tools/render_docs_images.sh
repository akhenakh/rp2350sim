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
cd "$ROOT"

render() {
	local board=$1 tab=$2 run=$3 name=$4
	shift 4
	local tmp="$ROOT/$BUILD/docs-$name-2x.png"
	local args=()
	for a in "$@"; do
		args+=("${a#"$ROOT"/}")
	done
	"$UI" --board "$board" --tab "$tab" --width 1280 --height 820 \
		--run "$run" --png "$tmp" "${args[@]}" >/dev/null
	ffmpeg -v error -y -i "$tmp" -vf scale=1280:820:flags=lanczos \
		"$IMAGES/$name.png"
}

render pico2-dvi dvi 0.5 dvi "$ROOT/$BUILD/pico-examples/build/dvi_out_hstx_encoder/dvi_out_hstx_encoder.elf"
render pico2-midi midi 1 midi "$ROOT/$BUILD/midi.elf"
render pico2-audio audio 1 audio "$ROOT/$BUILD/audio.elf"
render pico2-analog scope 0.02 scope
render pico2-analog logic 0.05 logic "$ROOT/$BUILD/protocols.elf"
render pico2 board 1 board "$ROOT/$BUILD/blinky.elf"
render pico2-inputs board 0.3 inputs --input btn1=on --input sw1=on --input bootsel=on --input pot1=2.2 --input enc1=+2 "$ROOT/firmware/c/build/inputs.elf"
render pico2b board 0.1 rp2350b
render geek lcd 0.6 lcd "$ROOT/$BUILD/lcd.elf"
render touch-lcd2 sensors 0.6 sensors --touch 120,200 "$ROOT/$BUILD/touchlcd.elf"
render pico2-ws2812 ws 1 ws2812 "$ROOT/$BUILD/ws2812.elf"
render pico2-psram board 0.6 psram "$ROOT/firmware/c/build/psram.elf"
render pico2 otp 0.5 otp "$ROOT/firmware/c/build/otp.elf"
render pico2 console 1 console "$ROOT/firmware/c/build/usbhello.elf"

# Debug and Bus need extra flags; the picker has no board.
"$UI" --board pico2 --tab debug --break debug_marker --debug-tab variables \
	--run 0.7 --width 1280 --height 820 \
	--png "$ROOT/$BUILD/docs-debug-2x.png" firmware/c/build/debug.elf >/dev/null
ffmpeg -v error -y -i "$ROOT/$BUILD/docs-debug-2x.png" \
	-vf scale=1280:820:flags=lanczos "$IMAGES/debug.png"

"$UI" --board pico2-bus --tab bus --bus-connect \
	--bus-answer 'i2c=ACK ACK 0x12 0x34' --bus-answer 'spi=0xEF 0x40 0x18' \
	--run 1 --width 1280 --height 820 --png "$ROOT/$BUILD/docs-bus-2x.png" \
	"$BUILD/busmaster.elf" >/dev/null
ffmpeg -v error -y -i "$ROOT/$BUILD/docs-bus-2x.png" \
	-vf scale=1280:820:flags=lanczos "$IMAGES/bus.png"

"$UI" --width 1280 --height 820 --png "$ROOT/$BUILD/docs-picker-2x.png" >/dev/null
ffmpeg -v error -y -i "$ROOT/$BUILD/docs-picker-2x.png" \
	-vf scale=1280:820:flags=lanczos "$IMAGES/picker.png"

# The CH9120 and virtual Wi-Fi demos connect to 127.0.0.1:9000; run a small
# echo server while their tabs are captured.
python3 -c '
import socket
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 9000))
s.listen(4)
while True:
    c, _ = s.accept()
    try:
        while True:
            d = c.recv(4096)
            if not d:
                break
            c.sendall(d)
    except OSError:
        pass
    finally:
        c.close()
' &
ECHO_PID=$!
trap 'kill "$ECHO_PID" 2>/dev/null || true' EXIT
sleep 0.3
render rp2350-eth eth 2 ethernet "$ROOT/$BUILD/ch9120.elf"
render pico2w eth 2 wifi "$ROOT/$BUILD/wifi.elf"
kill "$ECHO_PID" 2>/dev/null || true
wait "$ECHO_PID" 2>/dev/null || true
trap - EXIT

flash="${TMPDIR:-/tmp}/rp2350sim-docs-ota.bin"
rm -f "$flash"
"$UI" --board pico2 --flash "$flash" --run 0.5 --width 1280 --height 820 \
	--png "$ROOT/$BUILD/docs-ota-2x.png" firmware/c/build/ota.elf >/dev/null
ffmpeg -v error -y -i "$ROOT/$BUILD/docs-ota-2x.png" -vf scale=1280:820:flags=lanczos \
	"$IMAGES/ota.png"
rm -f "$flash"

echo "updated docs/images: dvi midi audio scope logic board inputs rp2350b lcd sensors ws2812 psram otp console debug bus picker ethernet wifi ota"
