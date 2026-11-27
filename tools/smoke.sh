#!/usr/bin/env bash
# smoke.sh runs built firmware in the emulator and asserts on observable
# behaviour. It is the integration gate behind `task ci`.
set -uo pipefail

BUILD=${1:-build}
EMU="$BUILD/rp2350sim"
# SMOKE_GROUP selects one parallel slice of the suite; the default "all" runs
# every slice in order for a standalone smoke run.
GROUP=${SMOKE_GROUP:-all}
in_group() { [ "$GROUP" = all ] || [ "$GROUP" = "$1" ]; }
in_pex() {
	in_group pex || in_group pex1 || in_group pex2 || in_group pex3 || in_group pex4
}
SMOKE_LOG=${SMOKE_LOG:-$BUILD/smoke.log}
fail=0
: >"$SMOKE_LOG"

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

if in_group basic1; then
# --- blinky: GP25 toggles at least four times in one virtual second ---------
out=$("$EMU" -fw "$BUILD/blinky.elf" -seconds 1 -trace-pins 25 2>&1)
n=$(grep -c '"msg":"pin change","pin":25,' <<<"$out")
if [ "$n" -ge 4 ]; then ok "blinky toggles GP25 ($n edges)"; else bad "blinky GP25" "$out"; fi

# --- PWM/GPIO audio decode and WAV export -----------------------------------
if [ -x "$BUILD/wavcheck" ]; then
	"$EMU" -fw "$BUILD/audio.elf" -seconds 1 -quiet -audio-pwm 0 \
		-audio-out "$BUILD/audio.wav" >/dev/null 2>&1
	if "$BUILD/wavcheck" -rate 48000 -channels 1 -freq 440 -freq-tol 30 \
		-min-rms 0.1 "$BUILD/audio.wav" >>"$SMOKE_LOG" 2>&1; then
		ok "PWM audio WAV"
	else
		bad "PWM audio WAV" "$(cat "$SMOKE_LOG")"
	fi
fi

# --- USB-MIDI class endpoint and monitor -------------------------------------
if [ -f "$BUILD/midi.elf" ]; then
	out=$("$EMU" -fw "$BUILD/midi.elf" -seconds 1 -usb-midi -midi-monitor 2>&1)
	contains "USB-MIDI note on" '"msg":"midi"' "$out"
fi

# --- USB HID keyboard, mouse and gamepad -------------------------------------
if [ -f "$BUILD/hid.elf" ]; then
	out=$("$EMU" -fw "$BUILD/hid.elf" -seconds 3 -usb-hid 2>&1)
	contains "USB HID keyboard" '"kind":"keyboard"' "$out"
	contains "USB HID mouse" '"kind":"mouse"' "$out"
fi
if [ -f "$BUILD/hidgamepad.elf" ]; then
	out=$("$EMU" -fw "$BUILD/hidgamepad.elf" -seconds 2 -usb-hid 2>&1)
	contains "USB HID gamepad" '"kind":"gamepad"' "$out"
fi

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

# --- microSD: stock TinyGo driver over SPI, FAT16 backed by a host dir ------
mkdir -p "$BUILD/sdcard-host"
printf 'hello from the host\n' >"$BUILD/sdcard-host/hello world.txt"
out=$("$EMU" -fw "$BUILD/sdcard.elf" -sd -sd-dir "$BUILD/sdcard-host" -seconds 4 -quiet 2>&1)
contains "SD card identity" "SDHC/SDXC card" "$out"
contains "SD FAT16 root directory" "file: hello world.txt 20 bytes" "$out"

# --- multicore scheduler ----------------------------------------------------
# pico2 defaults to the USB CDC console, so bytes printed before the host
# enumerates are dropped exactly like on hardware. The completion line, printed
# two 200 ms sleeps later, is the reliable proof that both cores ran tasks.
out=$("$EMU" -fw "$BUILD/cores.elf" -seconds 1.5 2>&1)
contains "core 1 runs tasks" "cores: done" "$out"
contains "core 1 launched" '"core1_launches":1' "$out"

# --- cores scheduler affinity ------------------------------------------------
out=$("$EMU" -fw "$BUILD/assigncore.elf" -seconds 1.5 2>&1)
contains "AssignCore pins core 0" "assigncore: core=0 ok=true" "$out"
contains "AssignCore pins core 1" "assigncore: core=1 ok=true" "$out"
contains "AssignCore survives sleeps and wakeups" "assigncore: done" "$out"

# --- ST7789 framebuffer export ---------------------------------------------
out=$("$EMU" -fw "$BUILD/lcd.elf" -seconds 3 -lcd -lcd-png "$BUILD/lcd-smoke.png" 2>&1)
if "$BUILD/pngcheck" -w 240 -h 135 -colors 2 "$BUILD/lcd-smoke.png" >>"$SMOKE_LOG" 2>&1; then
	ok "LCD framebuffer exported"
else
	bad "LCD framebuffer" "$out" "$(cat "$SMOKE_LOG")"
fi

# --- RP2350-Touch-LCD-2 -----------------------------------------------------
out=$("$EMU" -fw "$BUILD/touchlcd.elf" -seconds 1 -touch-lcd2 -lcd-png "$BUILD/touchlcd-smoke.png" 2>&1)
if "$BUILD/pngcheck" -w 320 -h 240 -colors 2 "$BUILD/touchlcd-smoke.png" >>"$SMOKE_LOG" 2>&1; then
	ok "Touch-LCD-2 framebuffer exported"
else
	bad "Touch-LCD-2 framebuffer" "$out" "$(cat "$SMOKE_LOG")"
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

fi

if in_group network; then
# --- W5500 Ethernet against the host helper --------------------------------
# ethhost drives its two server checks on wall time (1 s boot, then 300 ms
# apart); keep the virtual run long enough that a fast emulator does not exit
# before the second check is attempted.
if port_busy 9000; then
	echo "FAIL: port 9000 is already in use; kill any leftover ethhost first" >&2
	exit 1
fi
"$BUILD/ethhost" >"$BUILD/ethhost-w5500.log" 2>&1 &
host=$!
trap 'kill "$host" 2>/dev/null' EXIT
sleep 0.5
out=$("$EMU" -fw "$BUILD/w5500.elf" -seconds 12 -eth 2>&1)
kill "$host" 2>/dev/null || true
trap - EXIT
log=$(cat "$BUILD/ethhost-w5500.log")
contains "W5500 TCP echo" "tcp echo:" "$out"
contains "W5500 UDP" "udp: ok" "$out"
contains "W5500 TCP server" 'server check 1: reply "pong\n"' "$log"
contains "W5500 listener re-armed" 'server check 2: reply "pong\n"' "$log"
contains "W5500 network advertised" "tcp/9002 listen host=127.0.0.1:9002" "$out"

# --- W6300 Ethernet against the host helper --------------------------------
if port_busy 9000; then
	echo "FAIL: port 9000 is already in use; kill any leftover ethhost first" >&2
	exit 1
fi
"$BUILD/ethhost" -udp-reply >"$BUILD/ethhost-smoke.log" 2>&1 &
host=$!
trap 'kill "$host" 2>/dev/null' EXIT
sleep 0.5
out=$("$EMU" -fw "$BUILD/w6300.elf" -seconds 12 -w6300 2>&1)
log=$(cat "$BUILD/ethhost-smoke.log")
contains "W6300 TCP echo" "tcp echo:" "$out"
contains "W6300 TCP split read" "tcp split: ok" "$out"
contains "W6300 UDP" "udp: ok" "$out"
contains "W6300 UDP framing" 'udp datagrams: "first\n" "second\n"' "$out"
contains "W6300 TCP server" 'server check 1: reply "pong\n"' "$log"
contains "W6300 listener re-armed" 'server check 2: reply "pong\n"' "$log"
contains "W6300 network advertised" "tcp/9002 listen host=127.0.0.1:9002" "$out"
if (cd cmd/simui && go run . --board poe-eth --tab eth --run 0.5 \
	--png "../../$BUILD/eth-ui-smoke.png" "../../$BUILD/w6300.elf" >/dev/null 2>&1) &&
	"$BUILD/pngcheck" -w 2560 -h 1640 -colors 20 "$BUILD/eth-ui-smoke.png" >>"$SMOKE_LOG" 2>&1; then
	ok "UI Ethernet tab"
else
	bad "UI Ethernet tab" "$(cat "$SMOKE_LOG")"
fi
kill "$host" 2>/dev/null || true
trap - EXIT

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
contains "CH9120 network advertised" "tcp/2000 established" "$out"

# --- virtual Wi-Fi netdev --------------------------------------------------
"$BUILD/ethhost" >"$BUILD/ethhost-wifi.log" 2>&1 &
wifihost=$!
sleep 0.5
out=$("$EMU" -fw "$BUILD/wifi.elf" -seconds 3 -ch9120 2>&1)
kill "$wifihost" 2>/dev/null || true
contains "virtual Wi-Fi netdev" "wifi echo: hello wifi" "$out"
contains "virtual Wi-Fi network" "ip=10.0.0.5" "$out"

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
elif "$BUILD/serialcheck" -addr "$serialaddr" -send "serial-check" >>"$SMOKE_LOG" 2>&1; then
	ok "UART over TCP ($serialaddr)"
else
	bad "UART over TCP" "$(cat "$BUILD/serial.log")" "$(cat "$SMOKE_LOG")"
fi
kill "$serialpid" 2>/dev/null || true

# --- I2C/SPI TCP bridges: host fake device -------------------------------
rm -f "$BUILD/bus-master.out" "$BUILD/bus-master.log"
"$EMU" -fw "$BUILD/busmaster.elf" -seconds 5 \
	-i2c-tcp '0:0x50@127.0.0.1:0' -spi-tcp '0:17@127.0.0.1:0' \
	-expect 'i2c: 12 34' -expect 'spi: ef 40 18' -stop-on-expect \
	>"$BUILD/bus-master.out" 2>"$BUILD/bus-master.log" &
buspid=$!
i2caddr=""
spiaddr=""
for _ in $(seq 1 50); do
	i2caddr=$(grep '"msg":"bus bridge listening"' "$BUILD/bus-master.log" |
		grep '"kind":"i2c"' | sed -n 's/.*"addr":"\([^"]*\)".*/\1/p' | head -1)
	spiaddr=$(grep '"msg":"bus bridge listening"' "$BUILD/bus-master.log" |
		grep '"kind":"spi"' | sed -n 's/.*"addr":"\([^"]*\)".*/\1/p' | head -1)
	if [ -n "$i2caddr" ] && [ -n "$spiaddr" ]; then
		break
	fi
	sleep 0.1
done
if [ -n "$i2caddr" ] && [ -n "$spiaddr" ]; then
	"$BUILD/busfake" -kind i2c -addr "$i2caddr" >"$BUILD/busfake-i2c.log" 2>&1 &
	fakei2c=$!
	"$BUILD/busfake" -kind spi -addr "$spiaddr" >"$BUILD/busfake-spi.log" 2>&1 &
	fakespi=$!
	wait "$buspid"
	busrc=$?
	if [ "$busrc" -eq 0 ] &&
		grep -q "i2c: 12 34" "$BUILD/bus-master.out" &&
		grep -q "spi: ef 40 18" "$BUILD/bus-master.out"; then
		ok "I2C/SPI TCP bridge with a host device"
	else
		bad "I2C/SPI TCP bridge host device" "$(cat "$BUILD/bus-master.out")" \
			"$(tail -10 "$BUILD/bus-master.log")"
	fi
	kill "$fakei2c" "$fakespi" 2>/dev/null || true
else
	bad "I2C/SPI TCP bridge" "no bridge addresses" "$(tail -20 "$BUILD/bus-master.log")"
	kill "$buspid" 2>/dev/null || true
fi

# --- two simulators: master <-> UART device firmware ---------------------
rm -f "$BUILD/busdevice.out" "$BUILD/busdevice.log" "$BUILD/busmaster.out" "$BUILD/busmaster.log"
"$EMU" -fw "$BUILD/busdevice.elf" -seconds 300 \
	-serial-tcp '0:127.0.0.1:0,1:127.0.0.1:0' \
	>"$BUILD/busdevice.out" 2>"$BUILD/busdevice.log" &
bdevpid=$!
u0=""
u1=""
for _ in $(seq 1 50); do
	u0=$(grep '"msg":"uart bridge listening"' "$BUILD/busdevice.log" |
		grep '"uart":0' | sed -n 's/.*"addr":"\([^"]*\)".*/\1/p' | head -1)
	u1=$(grep '"msg":"uart bridge listening"' "$BUILD/busdevice.log" |
		grep '"uart":1' | sed -n 's/.*"addr":"\([^"]*\)".*/\1/p' | head -1)
	if [ -n "$u0" ] && [ -n "$u1" ]; then
		break
	fi
	sleep 0.1
done
if [ -n "$u0" ] && [ -n "$u1" ]; then
	"$EMU" -fw "$BUILD/busmaster.elf" -seconds 5 \
		-i2c-tcp "0:0x50=$u0" -spi-tcp "0:17=$u1" \
		-expect 'i2c: 12 34' -expect 'spi: ef 40 18' -stop-on-expect \
		>"$BUILD/busmaster.out" 2>"$BUILD/busmaster.log"
	rc=$?
	if [ "$rc" -eq 0 ] &&
		grep -q "i2c: 12 34" "$BUILD/busmaster.out" &&
		grep -q "spi: ef 40 18" "$BUILD/busmaster.out"; then
		ok "two sims over the I2C/SPI TCP bridges"
	else
		bad "two sims over the I2C/SPI TCP bridges" "$(cat "$BUILD/busmaster.out")" \
			"$(tail -10 "$BUILD/busmaster.log")" "$(tail -10 "$BUILD/busdevice.log")"
	fi
else
	bad "two sims over the I2C/SPI TCP bridges" "no UART bridge addresses" \
		"$(tail -20 "$BUILD/busdevice.log")"
fi
kill "$bdevpid" 2>/dev/null || true

fi

if in_group basic2; then
# --- built-in bus presets --------------------------------------------------
out=$("$EMU" -fw "$BUILD/presets.elf" -seconds 2 \
	-i2c-preset 0:bme280@0x76 -i2c-preset 0:mpu6050@0x68 \
	-i2c-preset 0:ssd1306@0x3c -spi-preset 0:w25q16@17 -quiet 2>/dev/null)
contains "BME280 preset" "bme280: 60" "$out"
contains "MPU6050 preset" "mpu6050: 68" "$out"
contains "SSD1306 preset" "ssd1306: ok" "$out"
contains "W25Q16 preset" "w25q16: ef 40 15" "$out"

if (cd cmd/simui && go run . --board pico2-presets --tab bus --run 0.05 \
	--png "../../$BUILD/ui-presets-smoke.png" >/dev/null 2>&1) &&
	"$BUILD/pngcheck" -w 2560 -h 1640 -colors 20 "$BUILD/ui-presets-smoke.png" >>"$SMOKE_LOG" 2>&1; then
	ok "UI built-in bus presets tab"
else
	bad "UI built-in bus presets tab" "$(cat "$SMOKE_LOG")"
fi

# --- scripted device plugins ------------------------------------------------
out=$("$EMU" -fw "$BUILD/presets.elf" -seconds 2 -quiet \
	-i2c-plugin 0:0x76=plugins/examples/bme280.lua \
	-spi-plugin 0:17=plugins/examples/w25q16.lua 2>/dev/null)
contains "Lua BME280 plugin" "bme280: 60" "$out"
contains "Lua W25Q16 plugin" "w25q16: ef 40 15" "$out"
out=$("$EMU" -fw "$BUILD/presets.elf" -seconds 2 -quiet \
	-spi-plugin 0:21=plugins/examples/spi-flash.wasm 2>/dev/null)
contains "WASM SPI flash plugin" "w25q16 wasm: ef 40 15" "$out"

out=$("$EMU" -fw firmware/c/build/itmdemo.elf -seconds 1 -quiet -itm 2>/dev/null)
contains "ITM stimulus trace" "itm: hello from ITM" "$out"
out=$("$EMU" -fw "$BUILD/itm.elf" -seconds 1 -quiet -itm 2>/dev/null)
contains "TinyGo ITM stimulus" "hello from TinyGo" "$out"
out=$("$EMU" -fw "$BUILD/can.elf" -seconds 2 -quiet -can 17:20:0x123 2>/dev/null)
contains "MCP2515 CAN" "can: tx=123 rx=124 data=42" "$out"
out=$("$EMU" -fw "$BUILD/lab.elf" -seconds 4 -quiet -onewire 2:22.5 -dht 3:dht22:45:23 \
	-ultrasonic 5:6:100 -gps 1 -can 13:14:0x123 -itm 2>/dev/null)
contains "LCD sensor lab 1-Wire" "lab onewire=22.50 C" "$out"
contains "LCD sensor lab DHT22" "lab dht=45.0% 23.0 C" "$out"
contains "LCD sensor lab HC-SR04" "lab distance=100 cm" "$out"
contains "LCD sensor lab GPS" "gps=true" "$out"
contains "LCD sensor lab CAN" "lab can tx=123 rx=124 data=42" "$out"
contains "LCD sensor lab ITM" "lab: itm tick" "$out"
out=$("$EMU" -fw "$BUILD/gps.elf" -seconds 2 -quiet -gps 1 2>/dev/null)
contains "NMEA GPS" "gps: fix=1 lat=45.501700 lon=-73.567300" "$out"
out=$("$EMU" -fw "$BUILD/dht.elf" -seconds 2 -quiet -dht 6:dht22:45:23 2>/dev/null)
contains "DHT22 sensor" "dht: 45.0 % 23.0 C" "$out"
out=$("$EMU" -fw "$BUILD/ultrasonic.elf" -seconds 2 -quiet -ultrasonic 6:7:100 2>/dev/null)
contains "HC-SR04 sensor" "ultrasonic: 100 cm" "$out"

out=$("$EMU" -fw "$BUILD/onewire.elf" -seconds 2 -quiet -onewire 6:22.5 2>/dev/null)
contains "1-Wire DS18B20" "onewire: 22.50 C" "$out"

out=$("$EMU" -fw "$BUILD/pluginlcd.elf" -seconds 2 -quiet \
	-i2c-plugin 0:0x76=plugins/examples/env-sensor.lua \
	-uart-plugin 1=plugins/examples/aq-sensor.wasm 2>/dev/null)
contains "Touch-LCD-2 plugin dashboard" "pluginlcd t=" "$out"
contains "UART air sensor values" "pm=" "$out"

# --- virtual CYW43 BLE controller -------------------------------------------
if [ -f "$BUILD/ble-c/ble_temp_reader.elf" ]; then
	rm -f "$BUILD/ble.pcapng"
	out=$("$EMU" -fw "$BUILD/ble-c/ble_temp_reader.elf" -ble -seconds 2 -quiet \
		-ble-hci-log "$BUILD/ble.pcapng" 2>&1)
	contains "BLE scan, connect and GATT notifications" "Read temp 23.45 degc" "$out"
	if [ -s "$BUILD/ble.pcapng" ]; then
		ok "BLE HCI pcapng capture"
	else
		bad "BLE HCI pcapng capture"
	fi
	if (cd cmd/simui && go run . --board pico2-w-ble --tab ble --run 1 \
		--png "../../$BUILD/ble-ui-smoke.png" "../../$BUILD/ble-c/ble_temp_reader.elf" >/dev/null 2>&1) &&
		"$BUILD/pngcheck" -w 2560 -h 1640 -colors 20 "$BUILD/ble-ui-smoke.png" >>"$SMOKE_LOG" 2>&1; then
		ok "UI BLE tab"
	else
		bad "UI BLE tab" "$(cat "$SMOKE_LOG")"
	fi
fi

if (cd cmd/simui && go run . --board pico2-plugins --tab bus --run 0.5 \
	--png "../../$BUILD/ui-plugins-smoke.png" "../../$BUILD/presets.elf" >/dev/null 2>&1) &&
	"$BUILD/pngcheck" -w 2560 -h 1640 -colors 20 "$BUILD/ui-plugins-smoke.png" >>"$SMOKE_LOG" 2>&1; then
	ok "UI scripted plugins tab"
else
	bad "UI scripted plugins tab" "$(cat "$SMOKE_LOG")"
fi

if (cd cmd/simui && go run . --board pico2-lcd-lab --tab lcd --run 3 \
	--png "../../$BUILD/ui-lab-smoke.png" "../../$BUILD/lab.elf" >/dev/null 2>&1) &&
	"$BUILD/pngcheck" -w 2560 -h 1640 -colors 20 "$BUILD/ui-lab-smoke.png" >>"$SMOKE_LOG" 2>&1; then
	ok "UI LCD sensor lab"
else
	bad "UI LCD sensor lab" "$(cat "$SMOKE_LOG")"
fi

if (cd cmd/simui && go run . --board pico2-gps --tab gps --run 1 \
	--png "../../$BUILD/ui-gps-smoke.png" "../../$BUILD/gps.elf" >/dev/null 2>&1) &&
	"$BUILD/pngcheck" -w 2560 -h 1640 -colors 20 "$BUILD/ui-gps-smoke.png" >>"$SMOKE_LOG" 2>&1; then
	ok "UI GPS tab"
else
	bad "UI GPS tab" "$(cat "$SMOKE_LOG")"
fi

if (cd cmd/simui && go run . --board pico2-can --tab can --run 1 \
	--png "../../$BUILD/ui-can-smoke.png" "../../$BUILD/can.elf" >/dev/null 2>&1) &&
	"$BUILD/pngcheck" -w 2560 -h 1640 -colors 20 "$BUILD/ui-can-smoke.png" >>"$SMOKE_LOG" 2>&1; then
	ok "UI CAN tab"
else
	bad "UI CAN tab" "$(cat "$SMOKE_LOG")"
fi

if [ -f firmware/c/build/itmdemo.elf ]; then
	if (cd cmd/simui && go run . --board pico2 --tab trace --run 1 \
		--png "../../$BUILD/ui-trace-smoke.png" "../../firmware/c/build/itmdemo.elf" >/dev/null 2>&1) &&
		"$BUILD/pngcheck" -w 2560 -h 1640 -colors 20 "$BUILD/ui-trace-smoke.png" >>"$SMOKE_LOG" 2>&1; then
		ok "UI Trace tab"
	else
		bad "UI Trace tab" "$(cat "$SMOKE_LOG")"
	fi
fi

if (cd cmd/simui && go run . --board pico2 --png "../../$BUILD/ui-smoke.png" --run 0.4 "../../$BUILD/blinky.elf" >/dev/null 2>&1) &&
	"$BUILD/pngcheck" -w 2560 -h 1640 -colors 20 "$BUILD/ui-smoke.png" >>"$SMOKE_LOG" 2>&1; then
	ok "UI headless snapshot"
else
	bad "UI headless snapshot" "$(cat "$SMOKE_LOG")"
fi

if (cd cmd/simui && go run . --board pico2-analog --tab scope --run 0.02 	--png "../../$BUILD/scope-smoke.png" >/dev/null 2>&1) &&
	"$BUILD/pngcheck" -w 2560 -h 1640 -colors 20 "$BUILD/scope-smoke.png" >>"$SMOKE_LOG" 2>&1; then
	ok "UI oscilloscope tab"
else
	bad "UI oscilloscope tab" "$(cat "$SMOKE_LOG")"
fi

fi

if in_group c || in_pex; then
# --- Pico SDK C firmware (optional: needs the ARM toolchain and the SDK) ------
if [ -n "${PICO_SDK_PATH:-}" ] && command -v arm-none-eabi-gcc >/dev/null 2>&1 &&
	command -v cmake >/dev/null 2>&1; then
	# A `task ci` dependency may have built the same examples already; reuse
	# them instead of compiling the SDK tree twice. Standalone runs build
	# their own copy under $BUILD/pico-c.
	C_BUILD=
	if [ -n "${PICO_C_BUILD:-}" ] && [ -f "$PICO_C_BUILD/hello.elf" ] &&
		[ -f "$PICO_C_BUILD/ota2.bin" ]; then
		C_BUILD=$PICO_C_BUILD
	elif cmake -S firmware/c -B "$BUILD/pico-c" -G Ninja -DPICO_BOARD=pico2 \
		-DPICO_SDK_PATH="$PICO_SDK_PATH" -DCMAKE_BUILD_TYPE=Release \
		-DPICO_NO_PICOTOOL=1 \
		>>"$SMOKE_LOG" 2>&1 &&
		cmake --build "$BUILD/pico-c" >>"$SMOKE_LOG" 2>&1; then
		C_BUILD="$BUILD/pico-c"
	fi
	if [ -n "$C_BUILD" ]; then
		if in_group c; then
		out=$("$EMU" -fw "$C_BUILD/hello.elf" -seconds 1 -quiet 2>&1)
		contains "Pico SDK C hello" "hello c done" "$out"
		out=$("$EMU" -fw "$C_BUILD/usbhello.elf" -seconds 1 -quiet 2>&1)
		contains "Pico SDK USB stdio" "hello usb done" "$out"
		out=$("$EMU" -fw "$C_BUILD/usbecho.elf" -seconds 1 -quiet \
			-input "Z" -input-at 300ms 2>&1)
		contains "Pico SDK USB echo ready" "usb echo ready" "$out"
		contains "Pico SDK USB echo input" "echo Z" "$out"
		out=$("$EMU" -fw "$C_BUILD/aontimer.elf" -seconds 2 -quiet \
			-expect "aon alarm 1001" -stop-on-expect 2>&1)
		contains "Pico SDK AON timer" "aon alarm 1001" "$out"
		out=$("$EMU" -fw "$C_BUILD/rtosctx.elf" -seconds 2 -quiet \
			-expect "rtosctx ok" -stop-on-expect 2>&1)
		contains "C RTOS context switch" "rtosctx ok a=5 b=5 nested=1 crit=1" "$out"
		out=$("$EMU" -fw "$C_BUILD/hang.elf" -seconds 1 -hang-cycles 500000 2>&1)
		contains "C hang detector" '"msg":"hang detected"' "$out"
		out=$("$EMU" -fw "$C_BUILD/inputs.elf" -seconds 0.5 -quiet 2>&1)
		contains "C board inputs demo" "inputs: btn=0 sw=0 enc=0 boot=0" "$out"
		contains "C board inputs float" "0.00 V" "$out"
		out=$("$EMU" -fw "$C_BUILD/floatdemo.elf" -seconds 1 -quiet 2>&1)
		contains "C single-precision FPU" "40700000 40580000 3fc00000 3f400000" "$out"
		out=$("$EMU" -fw "$C_BUILD/dcp.elf" -seconds 1 -quiet 2>&1)
		contains "C double DCP" "4010000000000000 c01e000000000000 4018000000000000 3fd5555555555555 3ff6a09e667f3bcd" "$out"
		out=$("$EMU" -fw "$C_BUILD/multicore.elf" -seconds 1 -quiet 2>&1)
		contains "C multicore" "worker=1" "$out"
		out=$("$EMU" -fw "$C_BUILD/otp.elf" -seconds 0.3 -quiet \
			-otp-dump "$BUILD/otp-smoke.json" 2>&1)
		contains "Pico SDK C OTP" "OTP demo: ok" "$out"
		if grep -q '"row": 1024' "$BUILD/otp-smoke.json" &&
			grep -q '"corrected"' "$BUILD/otp-smoke.json"; then
			ok "OTP dump reads back programmed rows"
		else
			bad "OTP dump" "$(cat "$BUILD/otp-smoke.json" 2>/dev/null)"
		fi
		out=$("$EMU" -fw "$C_BUILD/psram.elf" -seconds 2 -quiet \
			-psram 8 -psram-cs 8 -psram-latency 6 2>&1)
		contains "Pico SDK C PSRAM" "psram: ok" "$out"
		contains "Pico SDK C PSRAM size" "size=8388608" "$out"
		if grep -q "hang detected" <<<"$out"; then
			bad "Pico SDK C PSRAM idle" "$out"
		else
			ok "Pico SDK C PSRAM idle"
		fi
		if (cd cmd/simui && go run . --board pico2 --tab otp --run 0.05 --width 1920 --height 1080 \
			--png "../../$BUILD/otp-ui-smoke.png" "../../$C_BUILD/otp.elf" >/dev/null 2>&1) &&
			"$BUILD/pngcheck" -w 3840 -h 2160 -colors 20 "$BUILD/otp-ui-smoke.png" >>"$SMOKE_LOG" 2>&1; then
			ok "UI OTP tab"
		else
			bad "UI OTP tab" "$(cat "$SMOKE_LOG")"
		fi
		rm -f "$BUILD/ota-flash.bin" "$BUILD/ota-prefix.bin" "$BUILD/ota-ui-flash.bin"
		out=$("$EMU" -fw "$C_BUILD/ota.elf" -flash "$BUILD/ota-flash.bin" -seconds 2 \
			-expect "ota v2 running" -stop-on-expect 2>&1)
		contains "OTA self-update boots v2" "ota v2 running" "$out"
		contains "OTA reset logged" '"msg":"reset"' "$out"
		size=$(wc -c <"$C_BUILD/ota2.bin" | tr -d ' ')
		dd if="$BUILD/ota-flash.bin" of="$BUILD/ota-prefix.bin" bs=1 count="$size" 2>/dev/null
		if cmp -s "$BUILD/ota-prefix.bin" "$C_BUILD/ota2.bin"; then
			ok "OTA flash image matches v2"
		else
			bad "OTA flash image" "first $size bytes differ"
		fi
		out=$("$EMU" -flash "$BUILD/ota-flash.bin" -seconds 0.2 -quiet 2>&1)
		contains "OTA flash boots without ELF" "ota v2 running" "$out"
		if (cd cmd/simui && go run . --board pico2 --flash "../../$BUILD/ota-ui-flash.bin" --run 0.5 \
			--width 1920 --height 1080 --png "../../$BUILD/ota-ui-smoke.png" \
			"../../$C_BUILD/ota.elf" >/dev/null 2>&1) &&
			"$BUILD/pngcheck" -w 3840 -h 2160 -colors 20 "$BUILD/ota-ui-smoke.png" >>"$SMOKE_LOG" 2>&1; then
			ok "UI self-update banner"
		else
			bad "UI self-update banner" "$(cat "$SMOKE_LOG")"
		fi
		out=$("$EMU" -fw "$C_BUILD/blinky.elf" -seconds 0.6 -trace-pins 25 2>&1)
		n=$(grep -c '"msg":"pin change","pin":25,' <<<"$out")
		if [ "$n" -ge 2 ]; then ok "C blinky toggles GP25 ($n edges)"; else bad "C blinky GP25" "$out"; fi

		fi
	else
		if in_group c; then
			bad "Pico SDK C build" "$(tail -20 "$SMOKE_LOG")"
		fi
	fi

	if in_pex; then
		# --- pico-examples: stock SDK programs with known output and signals ---
		if [ "${PICO_EXAMPLES_READY:-0}" = 1 ] ||
			bash tools/picoexamples.sh "$BUILD/pico-examples" >>"$SMOKE_LOG" 2>&1; then
			EX="$BUILD/pico-examples/build"
			if in_group pex || in_group pex1; then
			out=$("$EMU" -fw "$EX/hello_serial/hello_serial.elf" -seconds 2.2 -quiet 2>&1)
			contains "pico-examples hello_serial" "Hello, world!" "$out"
			out=$("$EMU" -fw "$EX/hello_usb/hello_usb.elf" -seconds 1.2 -quiet 2>&1)
			contains "pico-examples hello_usb" "Hello, world!" "$out"
			out=$("$EMU" -fw "$EX/dev_hid_composite/dev_hid_composite.elf" -seconds 1 -bootsel -usb-hid 2>&1)
			contains "pico-examples HID keyboard" '"kind":"keyboard"' "$out"
			contains "pico-examples HID gamepad" '"kind":"gamepad"' "$out"
			out=$("$EMU" -fw "$EX/sha256/hello_sha256.elf" -seconds 3 -quiet \
				-expect "Success" -stop-on-expect 2>&1)
			contains "pico-examples SHA-256" "Success" "$out"
			out=$("$EMU" -fw "$EX/rand/rand.elf" -seconds 3 -quiet \
				-expect "All done" -stop-on-expect 2>&1)
			contains "pico-examples TRNG rand" "All done" "$out"
			out=$("$EMU" -fw "$EX/dvi_out_hstx_encoder/dvi_out_hstx_encoder.elf" -seconds 0.5 -quiet \
				-dvi-png "$BUILD/dvi-smoke.png" 2>&1)
			if "$BUILD/pngcheck" -w 640 -h 480 -colors 100 "$BUILD/dvi-smoke.png" >>"$SMOKE_LOG" 2>&1; then
				ok "pico-examples HSTX DVI frame"
			else
				bad "pico-examples HSTX DVI frame" "$("$BUILD/pngcheck" -w 640 -h 480 -colors 100 "$BUILD/dvi-smoke.png" 2>&1)"
			fi
			if (cd cmd/simui && go run . --board pico2-dvi --tab dvi --run 0.5 \
				--png "../../$BUILD/dvi-ui-smoke.png" \
				"../../$EX/dvi_out_hstx_encoder/dvi_out_hstx_encoder.elf" >/dev/null 2>&1) &&
				"$BUILD/pngcheck" -w 2560 -h 1640 -colors 100 "$BUILD/dvi-ui-smoke.png" >>"$SMOKE_LOG" 2>&1; then
				ok "UI DVI tab"
			else
				bad "UI DVI tab" "$(cat "$SMOKE_LOG")"
			fi
			fi

			if in_group pex || in_group pex2; then
			out=$("$EMU" -fw "$EX/hello_uart/hello_uart.elf" -seconds 0.5 -quiet 2>&1)
			contains "pico-examples hello_uart" "AB Hello, UART!" "$out"
			out=$("$EMU" -fw "$EX/gps_uart/gps_uart.elf" -seconds 3 -quiet -hang-cycles 0 -gps 1 2>/dev/null)
			contains "pico-examples GPS NMEA" "Valid GPS Data Received" "$out"
			contains "pico-examples GPS latitude" "Latitude: 45.501701" "$out"
			contains "pico-examples GPS longitude" "Longitude: -73.567299" "$out"
			out=$("$EMU" -fw "$EX/pio_onewire/pio_onewire.elf" -seconds 2 -quiet -onewire 15:22.5 2>/dev/null)
			contains "pico-examples PIO 1-Wire ROM" "9e06050403020128" "$out"
			contains "pico-examples PIO 1-Wire temperature" "0: 22.500000" "$out"
			out=$("$EMU" -fw "$EX/hello_dma/hello_dma.elf" -seconds 0.5 -quiet 2>&1)
			contains "pico-examples hello_dma" "Hello, world! (from DMA)" "$out"
			out=$("$EMU" -fw "$EX/hello_reset/hello_reset.elf" -seconds 0.5 -quiet 2>&1)
			contains "pico-examples hello_reset" "Hello, reset!" "$out"
			out=$("$EMU" -fw "$EX/hello_adc/hello_adc.elf" -seconds 0.5 -quiet 2>&1)
			contains "pico-examples hello_adc" "ADC Example, measuring GPIO26" "$out"
			contains "pico-examples hello_adc conversion" "Raw value: 0x000, voltage: 0.000000 V" "$out"
			out=$("$EMU" -fw "$EX/divider/hello_divider.elf" -seconds 0.5 -quiet 2>&1)
			contains "pico-examples divider" "123456/-321 = -384 remainder 192" "$out"
			contains "pico-examples divider async" "Async result 123456/-321 = -384 remainder 192" "$out"
			# 32 is the arithmetic answer; the old post-indexed LDRD bug made
			# dcp_dot consume the wrong elements and print 28.
			out=$("$EMU" -fw "$EX/hello_dcp/hello_dcp.elf" -seconds 0.5 -quiet 2>&1)
			contains "pico-examples DCP" "(1,2,3).(4,5,6)=32" "$out"
			fi

			if in_group pex || in_group pex3; then
			out=$("$EMU" -fw "$EX/hello_watchdog/hello_watchdog.elf" -seconds 1.5 -quiet 2>&1)
			contains "pico-examples watchdog boot" "Clean boot" "$out"
			contains "pico-examples watchdog reboot" "Rebooted by Watchdog!" "$out"
			out=$("$EMU" -fw "$EX/hello_multicore/hello_multicore.elf" -seconds 0.5 -quiet \
				-continue-after-halt 2>&1)
			contains "pico-examples multicore core 0" "It's all gone well on core 0!" "$out"
			contains "pico-examples multicore core 1" "Its all gone well on core 1!" "$out"
			out=$("$EMU" -fw "$EX/hello_timer/hello_timer.elf" -seconds 14 -quiet 2>&1)
			contains "pico-examples timer alarm" "Hello Timer!" "$out"
			contains "pico-examples timer callback" "fired!" "$out"
			contains "pico-examples timer repeat" "Repeat at " "$out"
			contains "pico-examples timer cancel" "cancelled... 1" "$out"
			contains "pico-examples timer done" "Done" "$out"

			# GPIO/PIO/PWM programs: measure the pin, not just the console.
			if "$EMU" -fw "$EX/blink/blink.elf" -seconds 1.2 -quiet \
				-logic-pins 25 -logic-rate 1000 -logic-out "$BUILD/pe-blink.txt" >/dev/null 2>&1 &&
				"$BUILD/pincheck" -pin 25 -freq 2 -duty 50 "$BUILD/pe-blink.txt" >>"$SMOKE_LOG" 2>&1; then
				ok "pico-examples blink GP25 at 2 Hz"
			else
				bad "pico-examples blink signal" "$(cat "$SMOKE_LOG")"
			fi
			if "$EMU" -fw "$EX/hello_pio/hello_pio.elf" -seconds 1.6 -quiet \
				-logic-pins 25 -logic-rate 1000 -logic-out "$BUILD/pe-hello-pio.txt" >/dev/null 2>&1 &&
				"$BUILD/pincheck" -pin 25 -freq 1 -duty 50 "$BUILD/pe-hello-pio.txt" >>"$SMOKE_LOG" 2>&1; then
				ok "pico-examples hello_pio GP25 at 1 Hz"
			else
				bad "pico-examples hello_pio signal" "$(cat "$SMOKE_LOG")"
			fi
			if "$EMU" -fw "$EX/pio_blink/pio_blink.elf" -seconds 1.2 -quiet \
				-logic-pins 3,4,5,6 -logic-rate 1000 -logic-out "$BUILD/pe-pio-blink.txt" >/dev/null 2>&1 &&
				"$BUILD/pincheck" -pin 3 -freq 4 -duty 50 "$BUILD/pe-pio-blink.txt" >>"$SMOKE_LOG" 2>&1 &&
				"$BUILD/pincheck" -pin 4 -freq 3 -duty 50 "$BUILD/pe-pio-blink.txt" >>"$SMOKE_LOG" 2>&1 &&
				"$BUILD/pincheck" -pin 5 -freq 2 -duty 50 "$BUILD/pe-pio-blink.txt" >>"$SMOKE_LOG" 2>&1 &&
				"$BUILD/pincheck" -pin 6 -freq 1 -duty 50 "$BUILD/pe-pio-blink.txt" >>"$SMOKE_LOG" 2>&1; then
				ok "pico-examples pio_blink 4/3/2/1 Hz"
			else
				bad "pico-examples pio_blink signals" "$(cat "$SMOKE_LOG")"
			fi
			if "$EMU" -fw "$EX/pio_squarewave/pio_squarewave.elf" -seconds 0.01 -quiet \
				-continue-after-halt -logic-pins 0 -logic-rate 150e6 \
				-logic-out "$BUILD/pe-squarewave.txt" >/dev/null 2>&1 &&
				"$BUILD/pincheck" -pin 0 -freq 15e6 -duty 50 "$BUILD/pe-squarewave.txt" >>"$SMOKE_LOG" 2>&1; then
				ok "pico-examples PIO square wave 15 MHz"
			else
				bad "pico-examples PIO square wave" "$(cat "$SMOKE_LOG")"
			fi
			if "$EMU" -fw "$EX/hello_pwm/hello_pwm.elf" -seconds 0.01 -quiet \
				-continue-after-halt -logic-pins 0,1 -logic-rate 150e6 \
				-logic-out "$BUILD/pe-pwm.txt" >/dev/null 2>&1 &&
				"$BUILD/pincheck" -pin 0 -freq 37.5e6 -duty 25 "$BUILD/pe-pwm.txt" >>"$SMOKE_LOG" 2>&1 &&
				"$BUILD/pincheck" -pin 1 -freq 37.5e6 -duty 75 "$BUILD/pe-pwm.txt" >>"$SMOKE_LOG" 2>&1; then
				ok "pico-examples PWM 25/75% at 37.5 MHz"
			else
				bad "pico-examples PWM signals" "$(cat "$SMOKE_LOG")"
			fi
			fi

			if in_group pex || in_group pex4; then
			out=$("$EMU" -fw "$EX/i2c_bus_scan/i2c_bus_scan.elf" -seconds 1 -quiet 2>&1)
			contains "pico-examples I2C scan completes" "Done." "$out"
			if [ "$(tr -cd '@' <<<"$out" | wc -c | tr -d ' ')" -eq 0 ]; then
				ok "pico-examples I2C empty bus NACKs every address"
			else
				bad "pico-examples I2C empty bus" "$out"
			fi
			out=$("$EMU" -fw "$EX/i2c_bus_scan/i2c_bus_scan.elf" -seconds 1 -quiet \
				-touch-lcd2 2>&1)
			if [ "$(tr -cd '@' <<<"$out" | wc -c | tr -d ' ')" -eq 2 ] &&
				grep -qF "10 .  .  .  .  .  @" <<<"$out" &&
				grep -qF "60 .  .  .  .  .  .  .  .  .  .  .  @" <<<"$out"; then
				ok "pico-examples I2C scan finds the Touch-LCD-2 devices"
			else
				bad "pico-examples I2C scan devices" "$out"
			fi
			fi
		else
			bad "pico-examples build" "$(tail -20 "$SMOKE_LOG")"
		fi
	fi
else
	echo "skip: Pico SDK C checks (set PICO_SDK_PATH and install the ARM toolchain)"
fi

fi

if in_group basic2; then
"$EMU" -fw "$BUILD/protocols.elf" -seconds 0.4 -quiet \
	-logic-pins 0,1 -logic-uart 0,1,115200 -logic-out "$BUILD/logic-capture.txt" \
	-vcd "$BUILD/logic-capture.vcd" >/dev/null 2>&1
if grep -q "rp2350sim logic analyzer trace" "$BUILD/logic-capture.txt" &&
	grep -q "# UART taps" "$BUILD/logic-capture.txt" &&
	grep -q "# samples" "$BUILD/logic-capture.txt"; then
	ok "CLI logic analyzer capture"
else
	bad "CLI logic analyzer capture" "$(head -20 "$BUILD/logic-capture.txt" 2>/dev/null)"
fi
if grep -q '^\$timescale 1ns \$end' "$BUILD/logic-capture.vcd" &&
	grep -q '^\$var wire 1 ! GP0 \$end' "$BUILD/logic-capture.vcd" &&
	grep -q '^\$enddefinitions \$end' "$BUILD/logic-capture.vcd"; then
	ok "CLI VCD export"
else
	bad "CLI VCD export" "$(head -20 "$BUILD/logic-capture.vcd" 2>/dev/null)"
fi

if (cd cmd/simui && go run . --board pico2-analog --tab logic --run 0.05 	--png "../../$BUILD/logic-smoke.png" "../../$BUILD/protocols.elf" >/dev/null 2>&1) &&
	"$BUILD/pngcheck" -w 2560 -h 1640 -colors 30 "$BUILD/logic-smoke.png" >>"$SMOKE_LOG" 2>&1; then
	ok "UI logic analyzer tab"
else
	bad "UI logic analyzer tab" "$(cat "$SMOKE_LOG")"
fi

if (cd cmd/simui && go run . --board pico2-bus --tab bus --run 0.05 \
	--png "../../$BUILD/bus-ui-smoke.png" "../../$BUILD/busmaster.elf" >/dev/null 2>&1) &&
	"$BUILD/pngcheck" -w 2560 -h 1640 -colors 20 "$BUILD/bus-ui-smoke.png" >>"$SMOKE_LOG" 2>&1; then
	ok "UI I2C/SPI bus tab"
else
	bad "UI I2C/SPI bus tab" "$(cat "$SMOKE_LOG")"
fi

# --- profiling: exact LCOV, JSON report and the sampled top list ------------
out=$("$EMU" -fw "$BUILD/blinky.elf" -seconds 1 -quiet -profile \
	-coverage "$BUILD/blinky.info" -profile-out "$BUILD/blinky-profile.json" \
	-profile-top 3 2>&1)
if grep -q "^LF:" "$BUILD/blinky.info" && grep -q "^FNDA:" "$BUILD/blinky.info"; then
	ok "profile LCOV coverage"
else
	bad "profile LCOV coverage" "$(head -20 "$BUILD/blinky.info")"
fi
if grep -q '"msg":"profile top"' <<<"$out" && grep -q '"total_instructions"' "$BUILD/blinky-profile.json"; then
	ok "profile top functions and JSON report"
else
	bad "profile top functions and JSON report" "$out"
fi

if (cd cmd/simui && go run . --board pico2 --tab profile --run 0.5 \
	--png "../../$BUILD/profile-ui-smoke.png" "../../$BUILD/blinky.elf" >/dev/null 2>&1) &&
	"$BUILD/pngcheck" -w 2560 -h 1640 -colors 20 "$BUILD/profile-ui-smoke.png" >>"$SMOKE_LOG" 2>&1; then
	ok "UI Profile tab"
else
	bad "UI Profile tab" "$(cat "$SMOKE_LOG")"
fi

fi

if [ "$fail" -ne 0 ]; then
	echo "smoke: FAILURES" >&2
	exit 1
fi
echo "smoke: all checks passed"
