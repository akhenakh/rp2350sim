#!/usr/bin/env bash
# smoke.sh runs built firmware in the emulator and asserts on observable
# behaviour. It is the integration gate behind `task ci`.
set -uo pipefail

BUILD=${1:-build}
EMU="$BUILD/rp2350sim"
fail=0
: >"$BUILD/smoke.log"

ok() { echo "ok: $1"; }
bad() {
	echo "FAIL: $1" >&2
	shift
	if [ "$#" -gt 0 ]; then
		printf '%s\n' "$@" | tail -20 >&2
	fi
	fail=1
}

# contains NAME WANT OUTPUT
contains() {
	if grep -qF -- "$2" <<<"$3"; then ok "$1"; else bad "$1" "missing: $2" "$3"; fi
}

# --- blinky: GP25 toggles at least four times in one virtual second ---------
out=$("$EMU" -fw "$BUILD/blinky.elf" -seconds 1 -trace-pins 25 2>&1)
n=$(grep -c "gp25=" <<<"$out")
if [ "$n" -ge 4 ]; then ok "blinky toggles GP25 ($n edges)"; else bad "blinky GP25" "$out"; fi

# --- console input over USB CDC --------------------------------------------
out=$("$EMU" -fw "$BUILD/echo.elf" -seconds 0.4 -input "smoke-input" 2>&1)
contains "console echo" "smoke-input" "$out"

# --- PIO API conformance ----------------------------------------------------
out=$("$EMU" -fw "$BUILD/piotest.elf" -seconds 0.4 2>&1)
contains "PIO conformance" "pio: all checks passed" "$out"

# --- WS2812B wire decode ----------------------------------------------------
out=$("$EMU" -fw "$BUILD/ws2812.elf" -seconds 0.8 -ws2812 0 2>&1)
contains "WS2812B colours" "#ff0000 #00ff00 #0000ff" "$out"

# --- multicore scheduler ----------------------------------------------------
out=$("$EMU" -fw "$BUILD/cores.elf" -seconds 1.5 2>&1)
contains "core 1 runs tasks" "cores: done" "$out"
contains "core 1 launched" "core1 launches=1" "$out"

# --- ST7789 framebuffer export ---------------------------------------------
out=$("$EMU" -fw "$BUILD/lcd.elf" -seconds 3 -lcd -lcd-png "$BUILD/lcd-smoke.png" 2>&1)
if "$BUILD/pngcheck" -w 240 -h 135 -colors 2 "$BUILD/lcd-smoke.png" >>"$BUILD/smoke.log" 2>&1; then
	ok "LCD framebuffer exported"
else
	bad "LCD framebuffer" "$out" "$(cat "$BUILD/smoke.log")"
fi

# --- W6300 Ethernet against the host helper --------------------------------
if command -v ss >/dev/null 2>&1 && ss -ltn 2>/dev/null | grep -q ':9000 '; then
	echo "FAIL: port 9000 is already in use; kill any leftover ethhost first" >&2
	exit 1
fi
"$BUILD/ethhost" >"$BUILD/ethhost-smoke.log" 2>&1 &
host=$!
trap 'kill "$host" 2>/dev/null' EXIT
sleep 0.5
out=$("$EMU" -fw "$BUILD/w6300.elf" -seconds 6 -w6300 2>&1)
kill "$host" 2>/dev/null || true
trap - EXIT
log=$(cat "$BUILD/ethhost-smoke.log")
contains "W6300 TCP echo" "tcp echo:" "$out"
contains "W6300 UDP" "udp: ok" "$out"
contains "W6300 TCP server" 'server check 1: reply "pong\n"' "$log"
contains "W6300 listener re-armed" 'server check 2: reply "pong\n"' "$log"

# --- UI headless snapshot ---------------------------------------------------
if (cd ui && go run . --board pico2 --png "../$BUILD/ui-smoke.png" --run 0.4 ../build/blinky.elf >/dev/null 2>&1) &&
	"$BUILD/pngcheck" -w 2560 -h 1640 -colors 20 "$BUILD/ui-smoke.png" >>"$BUILD/smoke.log" 2>&1; then
	ok "UI headless snapshot"
else
	bad "UI headless snapshot" "$(cat "$BUILD/smoke.log")"
fi

if [ "$fail" -ne 0 ]; then
	echo "smoke: FAILURES" >&2
	exit 1
fi
echo "smoke: all checks passed"
