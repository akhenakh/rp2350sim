#!/usr/bin/env bash
# picoexamples.sh builds a pinned subset of raspberrypi/pico-examples with the
# local Pico SDK. The selected programs are the ones the emulator claims to
# support; FreeRTOS and the hardware-dependent examples are left out on purpose.
#
# Usage: tools/picoexamples.sh [build-dir]
#   PICO_SDK_PATH        required, path to the Pico SDK
#   PICO_EXAMPLES_TAG    release tag to fetch (default sdk-2.3.1)
#   PICOTOOL_DIR         optional prebuilt picotool CMake package directory;
#                        the default skips picotool entirely
set -euo pipefail

DEST=${1:-build/pico-examples}
TAG=${PICO_EXAMPLES_TAG:-sdk-2.3.1}
: "${PICO_SDK_PATH:?PICO_SDK_PATH is not set}"
export PICO_SDK_PATH

# The SDK compiles pico_cxx_options even for C-only programs, so the ARM
# toolchain needs its C++ headers. Ubuntu's libstdc++-arm-none-eabi-newlib is
# skipped by --no-install-recommends unless it is named explicitly.
if ! printf '#include <cassert>\n' | arm-none-eabi-g++ -x c++ -E - >/dev/null 2>&1; then
	echo "picoexamples: arm-none-eabi-g++ cannot find its C++ headers; install libstdc++-arm-none-eabi-newlib" >&2
	exit 1
fi

case "$TAG" in
sdk-2.3.1) SHA=0d62f75bafc2c8120d3276c3343d1a9195e909e9 ;;
*) SHA=$TAG ;; # allow a raw commit sha
esac

SRC="$DEST/pico-examples-$SHA"
BUILD="$DEST/build"
mkdir -p "$DEST"
DEST=$(cd "$DEST" && pwd)
SRC="$DEST/pico-examples-$SHA"
BUILD="$DEST/build"

if [ ! -f "$SRC/CMakeLists.txt" ]; then
	tar="$DEST/pico-examples-$SHA.tar.gz"
	if [ ! -f "$tar" ]; then
		echo "fetching pico-examples $TAG ($SHA)"
		curl -fsSL -o "$tar" "https://codeload.github.com/raspberrypi/pico-examples/tar.gz/$SHA"
	fi
	rm -rf "$DEST/unpack"
	mkdir -p "$DEST/unpack"
	tar xzf "$tar" -C "$DEST/unpack" --strip-components=1
	mv "$DEST/unpack" "$SRC"
fi

cat >"$DEST/CMakeLists.txt" <<'EOF'
cmake_minimum_required(VERSION 3.13)
include($ENV{PICO_SDK_PATH}/pico_sdk_init.cmake)
project(rp2350sim_pico_examples C CXX ASM)
set(CMAKE_C_STANDARD 11)
pico_sdk_init()

# The stock examples call this helper from their CMakeLists; the URL is only
# useful for real boards.
function(example_auto_set_url target)
endfunction()

set(EX ${PICO_EXAMPLES_PATH})
add_subdirectory(${EX}/hello_world/serial hello_serial)
add_subdirectory(${EX}/hello_world/usb hello_usb)
add_subdirectory(${EX}/usb/device/dev_hid_composite dev_hid_composite)
add_subdirectory(${EX}/uart/hello_uart hello_uart)
add_subdirectory(${EX}/blink blink)
add_subdirectory(${EX}/timer/hello_timer hello_timer)
add_subdirectory(${EX}/multicore/hello_multicore hello_multicore)
add_subdirectory(${EX}/dma/hello_dma hello_dma)
add_subdirectory(${EX}/uart/gps_uart gps_uart)
add_subdirectory(${EX}/pio/onewire pio_onewire)
add_subdirectory(${EX}/pio/hello_pio hello_pio)
add_subdirectory(${EX}/pio/squarewave pio_squarewave)
add_subdirectory(${EX}/pio/pio_blink pio_blink)
add_subdirectory(${EX}/pwm/hello_pwm hello_pwm)
add_subdirectory(${EX}/adc/hello_adc hello_adc)
add_subdirectory(${EX}/reset/hello_reset hello_reset)
add_subdirectory(${EX}/watchdog/hello_watchdog hello_watchdog)
add_subdirectory(${EX}/i2c/bus_scan i2c_bus_scan)
add_subdirectory(${EX}/divider divider)
add_subdirectory(${EX}/dcp/hello_dcp hello_dcp)
add_subdirectory(${EX}/sha/sha256 sha256)
add_subdirectory(${EX}/system/rand rand)
add_subdirectory(${EX}/hstx/dvi_out_hstx_encoder dvi_out_hstx_encoder)
EOF

# The smoke suite consumes ELFs only. Skip picotool so the SDK never fetches
# and builds it: recent CMake releases fail that sub-build on the CI runner.
picotool=(-DPICO_NO_PICOTOOL=1)
if [ -n "${PICOTOOL_DIR:-}" ]; then
	picotool=(-Dpicotool_DIR="$PICOTOOL_DIR")
fi

cmake -S "$DEST" -B "$BUILD" -G Ninja \
	-DPICO_BOARD=pico2 \
	-DPICO_SDK_PATH="$PICO_SDK_PATH" \
	-DPICO_EXAMPLES_PATH="$SRC" \
	-DCMAKE_BUILD_TYPE=Release \
	"${picotool[@]}"
cmake --build "$BUILD"
echo "$BUILD"
