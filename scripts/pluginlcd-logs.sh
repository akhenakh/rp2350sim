#!/bin/sh
# Show the device-plugin logs next to the firmware's own dashboard output.
set -u
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
"$root/build/rp2350sim" -fw "$root/build/pluginlcd.elf" -seconds 3 -quiet \
	-i2c-plugin "0:0x76=$root/plugins/examples/env-sensor.lua" \
	-uart-plugin "1=$root/plugins/examples/aq-sensor.wasm" 2>&1 |
	tr -d 'IR\r' |
	grep -E '"msg":"plugin"|pluginlcd' |
	sed 's/.*"line":"//; s/"}$//'
