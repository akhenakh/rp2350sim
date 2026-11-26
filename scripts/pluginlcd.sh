#!/bin/sh
# Run the Touch-LCD-2 plugin dashboard headlessly and keep only the firmware's
# own log lines. UART1 is mirrored to stdout by the runner, so strip the I/R
# request prefixes before matching.
set -u
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
"$root/build/rp2350sim" -fw "$root/build/pluginlcd.elf" -seconds 2 -quiet \
	-i2c-plugin "0:0x76=$root/plugins/examples/env-sensor.lua" \
	-uart-plugin "1=$root/plugins/examples/aq-sensor.wasm" 2>/dev/null |
	grep -o 'pluginlcd.*'
