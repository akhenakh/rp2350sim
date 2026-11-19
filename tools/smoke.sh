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
n=$(grep -c '"msg":"pin change","pin":25,' <<<"$out")
if [ "$n" -ge 4 ]; then ok "blinky toggles GP25 ($n edges)"; else bad "blinky GP25" "$out"; fi

# --- console input over USB CDC --------------------------------------------
out=$("$EMU" -fw "$BUILD/echo.elf" -seconds 0.4 -input "smoke-input" 2>&1)
contains "console echo" "smoke-input" "$out"

# --- automation contract ----------------------------------------------------
printf 'ci-contract' | "$EMU" -fw "$BUILD/echo.elf" -stdin -seconds 1 -quiet \
	-expect ci-contract -stop-on-expect -summary-file "$BUILD/contract.json" \
	>"$BUILD/contract.out" 2>"$BUILD/contract.err"
if [ $? -eq 0 ] && grep -q "ci-contract" "$BUILD/contract.out" &&
	grep -q '"exit_code": 0' "$BUILD/contract.json"; then
	ok "automation contract (stdin, expect, summary)"
else
	bad "automation contract" "$(cat "$BUILD/contract.err")" "$(cat "$BUILD/contract.out")"
fi
"$EMU" -fw "$BUILD/echo.elf" -seconds 0.2 -quiet -expect "impossible-string" >/dev/null 2>&1
if [ $? -eq 4 ]; then ok "missing expectation exit code"; else bad "missing expectation exit code"; fi
printf '\x00\x10\x00\x20\x09\x00\x00\x10\x00\xde\x00\xbf' >"$BUILD/fault.bin"
"$EMU" -fw "$BUILD/fault.bin" -seconds 0.05 -quiet >/dev/null 2>&1
if [ $? -eq 3 ]; then ok "halt exit code"; else bad "halt exit code"; fi

# --- PIO API conformance ----------------------------------------------------
out=$("$EMU" -fw "$BUILD/piotest.elf" -seconds 0.4 2>&1)
contains "PIO conformance" "pio: all checks passed" "$out"

# --- WS2812B wire decode ----------------------------------------------------
out=$("$EMU" -fw "$BUILD/ws2812.elf" -seconds 0.8 -ws2812 0 2>&1)
contains "WS2812B colours" "#ff0000 #00ff00 #0000ff" "$out"

# --- multicore scheduler ----------------------------------------------------
out=$("$EMU" -fw "$BUILD/cores.elf" -seconds 1.5 2>&1)
contains "core 1 runs tasks" "cores: done" "$out"
contains "core 1 launched" '"core1_launches":1' "$out"

# --- ST7789 framebuffer export ---------------------------------------------
out=$("$EMU" -fw "$BUILD/lcd.elf" -seconds 3 -lcd -lcd-png "$BUILD/lcd-smoke.png" 2>&1)
if "$BUILD/pngcheck" -w 240 -h 135 -colors 2 "$BUILD/lcd-smoke.png" >>"$BUILD/smoke.log" 2>&1; then
	ok "LCD framebuffer exported"
else
	bad "LCD framebuffer" "$out" "$(cat "$BUILD/smoke.log")"
fi

# --- RP2350-Touch-LCD-2 -----------------------------------------------------
out=$("$EMU" -fw "$BUILD/touchlcd.elf" -seconds 1 -touch-lcd2 -lcd-png "$BUILD/touchlcd-smoke.png" 2>&1)
if "$BUILD/pngcheck" -w 320 -h 240 -colors 2 "$BUILD/touchlcd-smoke.png" >>"$BUILD/smoke.log" 2>&1; then
	ok "Touch-LCD-2 framebuffer exported"
else
	bad "Touch-LCD-2 framebuffer" "$out" "$(cat "$BUILD/smoke.log")"
fi
out=$("$EMU" -fw "$BUILD/touchlcd.elf" -seconds 3 -quiet -touch-lcd2 \
	-touch 120,200 -touch-at 1s -touch-hold 500ms 2>&1)
contains "CST816D touch" "touchlcd touch x=120 y=200" "$out"
contains "QMI8658 IMU" "touchlcd imu ax=0 ay=0 az=4096" "$out"
contains "battery ADC" "touchlcd battery raw=25808 mv=3897" "$out"
out=$("$EMU" -fw "$BUILD/touchlcd.elf" -seconds 0.6 -quiet -touch-lcd2 -sensors 2>&1)
contains "Touch-LCD-2 sensors" '"sd_blocks":32768' "$out"

# --- SD card backed by a host directory -------------------------------------
mkdir -p "$BUILD/sdhost"
printf 'host file\n' >"$BUILD/sdhost/HOST.TXT"
out=$("$EMU" -fw "$BUILD/lcd.elf" -seconds 0.3 -quiet -lcd -sd \
	-sd-dir "$BUILD/sdhost" -sensors 2>&1)
contains "SD directory backing" '"sd_source":"' "$out"
if grep -q "$BUILD/sdhost" <<<"$out"; then
	ok "SD directory source"
else
	bad "SD directory source" "$out"
fi

# port_busy PORT reports whether a local TCP listener already owns PORT.
# ss is Linux-only; lsof covers macOS and the BSDs.
port_busy() {
	if command -v ss >/dev/null 2>&1; then
		ss -ltn 2>/dev/null | grep -q ":$1 "
	elif command -v lsof >/dev/null 2>&1; then
		lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
	else
		return 1
	fi
}

# --- W5500 Ethernet against the host helper --------------------------------
if port_busy 9000; then
	echo "FAIL: port 9000 is already in use; kill any leftover ethhost first" >&2
	exit 1
fi
"$BUILD/ethhost" >"$BUILD/ethhost-w5500.log" 2>&1 &
host=$!
trap 'kill "$host" 2>/dev/null' EXIT
sleep 0.5
out=$("$EMU" -fw "$BUILD/w5500.elf" -seconds 6 -eth 2>&1)
kill "$host" 2>/dev/null || true
trap - EXIT
log=$(cat "$BUILD/ethhost-w5500.log")
contains "W5500 TCP echo" "tcp echo:" "$out"
contains "W5500 UDP" "udp: ok" "$out"
contains "W5500 TCP server" 'server check 1: reply "pong\n"' "$log"
contains "W5500 listener re-armed" 'server check 2: reply "pong\n"' "$log"

# --- W6300 Ethernet against the host helper --------------------------------
if port_busy 9000; then
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
# --- CH9120 UART-to-Ethernet ----------------------------------------------
if port_busy 9000; then
	echo "FAIL: port 9000 is already in use; kill any leftover ethhost first" >&2
	exit 1
fi
"$BUILD/ethhost" >"$BUILD/ethhost-ch9120.log" 2>&1 &
chhost=$!
sleep 0.5
out=$("$EMU" -fw "$BUILD/ch9120.elf" -seconds 1.5 -ch9120 2>&1)
kill "$chhost" 2>/dev/null || true
contains "CH9120 TCP echo" "echo: hello ch9120" "$out"

# --- virtual Wi-Fi netdev --------------------------------------------------
"$BUILD/ethhost" >"$BUILD/ethhost-wifi.log" 2>&1 &
wifihost=$!
sleep 0.5
out=$("$EMU" -fw "$BUILD/wifi.elf" -seconds 3 -ch9120 2>&1)
kill "$wifihost" 2>/dev/null || true
contains "virtual Wi-Fi netdev" "wifi echo: hello wifi" "$out"

# --- UART over TCP ----------------------------------------------------------
"$EMU" -fw "$BUILD/serialbridge.elf" -seconds 4 -serial-tcp 1:127.0.0.1:0 \
	>"$BUILD/serial.log" 2>&1 &
serialpid=$!
serialaddr=""
for i in $(seq 1 50); do
	serialaddr=$(grep '"msg":"uart bridge listening"' "$BUILD/serial.log" |
		sed -n 's/.*"addr":"\([^"]*\)".*/\1/p' | head -1)
	if [ -n "$serialaddr" ]; then
		break
	fi
	sleep 0.1
done
if [ -z "$serialaddr" ]; then
	bad "UART over TCP" "no listener address" "$(cat "$BUILD/serial.log")"
elif "$BUILD/serialcheck" -addr "$serialaddr" -send "serial-check" >>"$BUILD/smoke.log" 2>&1; then
	ok "UART over TCP ($serialaddr)"
else
	bad "UART over TCP" "$(cat "$BUILD/serial.log")" "$(cat "$BUILD/smoke.log")"
fi
kill "$serialpid" 2>/dev/null || true

if (cd cmd/simui && go run . --board pico2 --png "../../$BUILD/ui-smoke.png" --run 0.4 "../../$BUILD/blinky.elf" >/dev/null 2>&1) &&
	"$BUILD/pngcheck" -w 2560 -h 1640 -colors 20 "$BUILD/ui-smoke.png" >>"$BUILD/smoke.log" 2>&1; then
	ok "UI headless snapshot"
else
	bad "UI headless snapshot" "$(cat "$BUILD/smoke.log")"
fi

if (cd cmd/simui && go run . --board pico2-analog --tab scope --run 0.02 	--png "../../$BUILD/scope-smoke.png" >/dev/null 2>&1) &&
	"$BUILD/pngcheck" -w 2560 -h 1640 -colors 20 "$BUILD/scope-smoke.png" >>"$BUILD/smoke.log" 2>&1; then
	ok "UI oscilloscope tab"
else
	bad "UI oscilloscope tab" "$(cat "$BUILD/smoke.log")"
fi

# --- Pico SDK C firmware (optional: needs the ARM toolchain and the SDK) ------
if [ -n "${PICO_SDK_PATH:-}" ] && command -v arm-none-eabi-gcc >/dev/null 2>&1 &&
	command -v cmake >/dev/null 2>&1; then
	if cmake -S firmware/c -B "$BUILD/pico-c" -G Ninja -DPICO_BOARD=pico2 \
		-DPICO_SDK_PATH="$PICO_SDK_PATH" -DCMAKE_BUILD_TYPE=Release \
		>>"$BUILD/smoke.log" 2>&1 &&
		cmake --build "$BUILD/pico-c" >>"$BUILD/smoke.log" 2>&1; then
		out=$("$EMU" -fw "$BUILD/pico-c/hello.elf" -seconds 1 -quiet 2>&1)
		contains "Pico SDK C hello" "hello c done" "$out"
		out=$("$EMU" -fw "$BUILD/pico-c/floatdemo.elf" -seconds 1 -quiet 2>&1)
		contains "C single-precision FPU" "40700000 40580000 3fc00000 3f400000" "$out"
		out=$("$EMU" -fw "$BUILD/pico-c/multicore.elf" -seconds 1 -quiet 2>&1)
		contains "C multicore" "worker=1" "$out"
		out=$("$EMU" -fw "$BUILD/pico-c/blinky.elf" -seconds 0.6 -trace-pins 25 2>&1)
		n=$(grep -c '"msg":"pin change","pin":25,' <<<"$out")
		if [ "$n" -ge 2 ]; then ok "C blinky toggles GP25 ($n edges)"; else bad "C blinky GP25" "$out"; fi
	else
		bad "Pico SDK C build" "$(tail -20 "$BUILD/smoke.log")"
	fi
else
	echo "skip: Pico SDK C checks (set PICO_SDK_PATH and install the ARM toolchain)"
fi

if (cd cmd/simui && go run . --board pico2-analog --tab logic --run 0.05 	--png "../../$BUILD/logic-smoke.png" "../../$BUILD/protocols.elf" >/dev/null 2>&1) &&
	"$BUILD/pngcheck" -w 2560 -h 1640 -colors 30 "$BUILD/logic-smoke.png" >>"$BUILD/smoke.log" 2>&1; then
	ok "UI logic analyzer tab"
else
	bad "UI logic analyzer tab" "$(cat "$BUILD/smoke.log")"
fi

if [ "$fail" -ne 0 ]; then
	echo "smoke: FAILURES" >&2
	exit 1
fi
echo "smoke: all checks passed"
