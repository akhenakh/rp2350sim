#!/usr/bin/env bash
# Optional Zephyr smoke test. It needs a west workspace (default
# $HOME/zephyrproject) and the Zephyr SDK (default /opt/zephyr-sdk), so it is
# not part of `task ci`. Build the emulator first (`task build`).
set -uo pipefail

BUILD=${1:-build}
WS=${ZEPHYR_WORKSPACE:-$HOME/zephyrproject}
VENV=${ZEPHYR_VENV:-$HOME/zephyr-venv}
WEST=${ZEPHYR_WEST:-$VENV/bin/west}
: "${ZEPHYR_SDK_INSTALL_DIR:=/opt/zephyr-sdk}"
export ZEPHYR_SDK_INSTALL_DIR
ROOT=$(cd "$(dirname "$0")/.." && pwd)
case "$BUILD" in
/*) EMU=$BUILD/rp2350sim OUT=$BUILD/zephyr ;;
*) EMU=$ROOT/$BUILD/rp2350sim OUT=$ROOT/$BUILD/zephyr ;;
esac
BOARD=rpi_pico2/rp2350a/m33

if [ ! -x "$WEST" ]; then
	echo "west not found at $WEST (set ZEPHYR_VENV or ZEPHYR_WEST)" >&2
	exit 1
fi
if [ ! -f "$ZEPHYR_SDK_INSTALL_DIR/sdk_version" ]; then
	echo "Zephyr SDK not found at $ZEPHYR_SDK_INSTALL_DIR" >&2
	exit 1
fi
if [ ! -d "$WS/zephyr" ]; then
	echo "west workspace not found at $WS (set ZEPHYR_WORKSPACE)" >&2
	exit 1
fi
if [ ! -x "$EMU" ]; then
	echo "emulator not found at $EMU (run task build)" >&2
	exit 1
fi

fail=0
ok() { echo "ok: $1"; }
bad() {
	echo "FAIL: $1" >&2
	shift
	if [ "$#" -gt 0 ]; then
		printf '%s\n' "$@" | tail -20 >&2
	fi
	fail=1
}
contains() {
	if grep -qF -- "$2" <<<"$3"; then ok "$1"; else bad "$1" "missing: $2" "$3"; fi
}
mkdir -p "$OUT"

# zbuild NAME APP [extra west args...]
zbuild() {
	local name=$1 app=$2
	shift 2
	(cd "$WS" && "$WEST" build -p always -d "$OUT/$name" -b "$BOARD" "$app" "$@") \
		>"$OUT/$name-build.log" 2>&1
}

# run NAME [emu args...]
run() {
	local name=$1
	shift
	"$EMU" -fw "$OUT/$name/zephyr/zephyr.elf" -seconds 20 -wall-timeout 180s -quiet "$@" 2>&1
}

if zbuild hello zephyr/samples/hello_world; then
	out=$(run hello -expect "Hello World! rpi_pico2" -stop-on-expect)
	contains "Zephyr hello_world" "Hello World! rpi_pico2" "$out"
else
	bad "Zephyr hello_world build" "$(tail -20 "$OUT/hello-build.log")"
fi

if zbuild threads zephyr/samples/synchronization; then
	out=$(run threads -expect "thread_a" -expect "thread_b" -stop-on-expect)
	contains "Zephyr threads" "thread_a" "$out"
	contains "Zephyr threads" "thread_b" "$out"
else
	bad "Zephyr synchronization build" "$(tail -20 "$OUT/threads-build.log")"
fi

if zbuild blinky zephyr/samples/basic/blinky; then
	out=$(run blinky -seconds 1 -trace-pins 25)
	n=$(grep -c '"msg":"pin change","pin":25,' <<<"$out")
	if [ "$n" -ge 2 ]; then ok "Zephyr blinky GP25 ($n edges)"; else bad "Zephyr blinky GP25" "$out"; fi
else
	bad "Zephyr blinky build" "$(tail -20 "$OUT/blinky-build.log")"
fi

if zbuild fpu zephyr/tests/kernel/fpu_sharing/generic -- -DPI_NUM_ITERATIONS=500; then
	out=$(run fpu -seconds 30 -expect "PROJECT EXECUTION SUCCESSFUL" -stop-on-expect)
	contains "Zephyr FPU sharing" "PROJECT EXECUTION SUCCESSFUL" "$out"
else
	bad "Zephyr fpu_sharing build" "$(tail -20 "$OUT/fpu-build.log")"
fi

if zbuild context zephyr/tests/kernel/context; then
	out=$(run context -seconds 10 -expect "PROJECT EXECUTION SUCCESSFUL" -stop-on-expect)
	contains "Zephyr kernel context" "PROJECT EXECUTION SUCCESSFUL" "$out"
else
	bad "Zephyr context build" "$(tail -20 "$OUT/context-build.log")"
fi

if zbuild sched zephyr/tests/kernel/sched/schedule_api; then
	out=$(run sched -seconds 25 -expect "PROJECT EXECUTION SUCCESSFUL" -stop-on-expect)
	contains "Zephyr schedule_api" "PROJECT EXECUTION SUCCESSFUL" "$out"
else
	bad "Zephyr schedule_api build" "$(tail -20 "$OUT/sched-build.log")"
fi

if zbuild phil zephyr/samples/philosophers; then
	out=$(run phil -seconds 1 -expect "Philosopher" -stop-on-expect)
	contains "Zephyr philosophers" "Philosopher" "$out"
else
	bad "Zephyr philosophers build" "$(tail -20 "$OUT/phil-build.log")"
fi

if [ "$fail" -ne 0 ]; then
	exit 1
fi
echo "zephyr: all checks passed"
