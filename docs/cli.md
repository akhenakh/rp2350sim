# Command-line reference

```
rp2350sim -fw IMAGE [flags]
```

`-fw` accepts a `.elf`, `.uf2` or raw `.bin` image. UF2 family IDs are
validated and picotool's RP2350-E10 ignore block is skipped. The flash model
is 16 MiB.

The machine boots from the vector table at `0x10000000`; no RP2350 bootrom is
needed. The image must contain that vector table, with SP inside SRAM and the
reset vector in flash or SRAM. SRAM-resident and partition-table layouts are
rejected with a load error instead of being executed.

## Output contract

| stream | contents |
|---|---|
| **stdout** | only the firmware console (USB CDC, UART0/UART1) |
| **stderr** | emulator diagnostics as `slog` records: header, traces, peripheral logs, summary, errors |

So `rp2350sim -fw fw.elf -seconds 2 > firmware.log` produces a clean firmware
log. `-quiet` suppresses the informational records (header and final summary);
warnings, errors and halt reasons are still written.

## Logging

Diagnostics are structured `slog` records. The format is chosen by
`-log-format`:

| flag | meaning |
|---|---|
| `-log-format auto` | JSON when stderr is not a terminal or `CI` is set, text otherwise (default) |
| `-log-format json` | one JSON object per line, for CI and log collectors |
| `-log-format text` | `key=value` lines, for a person in a terminal |
| `-log-level LEVEL` | `debug`, `info` (default), `warn` or `error` |

A piped or CI run is JSON without any extra flags:

```sh
rp2350sim -fw fw.elf -seconds 2 2> emulator.jsonl > firmware.log
jq -r 'select(.msg=="run finished") | .cycles' emulator.jsonl
```

```json
{"time":"2026-11-18T14:57:16Z","level":"INFO","msg":"firmware loaded","path":"fw.elf","format":"elf","segments":3,"bytes":10780,"entry":"0x10001a81"}
{"time":"2026-11-18T14:57:18Z","level":"INFO","msg":"run finished","cycles":30000000,"virtual_seconds":0.2,"wall_seconds":0.99}
{"time":"2026-11-18T14:57:18Z","level":"INFO","msg":"core0","pc":"0x10000dec","sp":"0x20003850","lr":"0x10001f95"}
```

Addresses and register values are hex strings; counts and durations are
numbers (`wall_seconds` is a float). `-log-level debug` adds instruction
traces, PIO state-machine steps, USB enumeration and network socket activity.

## Exit codes

| code | meaning |
|---|---|
| `0` | the run completed (or `-stop-on-expect` matched) |
| `1` | load/configuration error, unreadable `-input-file`, summary write failed |
| `2` | usage error (unknown flag, missing `-fw`, conflicting flags, malformed numeric/debug/logic input) |
| `3` | a core halted or faulted (unimplemented instruction, UDF, unmapped MMIO, outside image) |
| `4` | one or more `-expect` strings were not seen |
| `5` | `-wall-timeout` expired |

Use `-allow-halt` to make code `3` exit `0` (useful while developing the CPU
core).

## Running and timing

| flag | meaning |
|---|---|
| `-fw PATH` | firmware image (`.elf`, `.uf2`, `.bin`) |
| `-seconds N` | virtual seconds to run (150 MHz clock); overrides `-steps` |
| `-steps N` | cycle budget (default 50,000,000) |
| `-core1` | start core 1 (default true; parked if false) |
| `-wall-timeout D` | fail with code 5 after this much real time (0 = unlimited) |
| `-quiet` | suppress the informational header and final summary records |
| `-allow-halt` | exit 0 even when a core halts |
| `-summary-file PATH` | write a JSON run summary |

The machine always starts at cycle 0; `-seconds` is **virtual** time, not wall
time. The emulator usually runs faster than the emulated clock, so a 3 s run
may take less than 3 s of wall time.

### Summary JSON

`-summary-file` writes:

```json
{
  "firmware": "firmware.elf",
  "format": "elf",
  "cycles": 75200000,
  "virtual_seconds": 0.501333,
  "wall_seconds": 1.842,
  "halted": false,
  "core0_pc": 268439026,
  "core0_sp": 536885328,
  "core1_pc": 0,
  "core1_sp": 0,
  "core1_parked": true,
  "irqs": 12,
  "core1_launches": 0,
  "expectations_matched": false,
  "wall_timed_out": false,
  "psram_bytes": 8388608,
  "psram_latency_cycles": 6,
  "exit_code": 0
}
```

`expectations_matched` is `true` only when at least one `-expect` was given and
all of them matched. `missing_expectations` and `halt_reason` appear when
relevant.

## Console input

| flag | meaning |
|---|---|
| `-input TEXT` | send bytes to USB CDC and UART0 |
| `-input-file PATH` | send the file contents (appended after `-input`) |
| `-input-at D` | delay before the input is sent (default `200ms`) |
| `-stdin` | forward host stdin to USB CDC and UART0 |
| `-expect TEXT` | fail (code 4) unless this console text appears; repeatable |
| `-stop-on-expect` | stop as soon as all `-expect` strings have appeared |

`-input` sends the literal bytes you pass; it does not expand `\n`. Use the
shell's ANSI-C quoting (`$'status\n'`), `-input-file`, or `-stdin` for a
line ending. `-input`/`-input-file` are useful for firmware that waits for a
command after boot; `-stdin` is for scripted sessions:

```sh
printf 'help\nstatus\n' | rp2350sim -fw fw.elf -seconds 3 -stdin
```

Input is delivered to **USB CDC** and **UART0** simultaneously. USB is
flow-controlled: bytes wait until the emulated host has enumerated the device
and the previous packet has been consumed. The default `-input-at 200ms` covers
USB enumeration; increase it for slow boot paths.

`-expect` matches the **firmware console** (stdout), not emulator diagnostics.
Matching is streaming, so it works across chunk boundaries. Combined with
`-stop-on-expect` it gives fast CI assertions:

```sh
rp2350sim -fw fw.elf -seconds 5 -input $'status\n' \
  -expect 'status: ok' -stop-on-expect -quiet > fw.log
```

## Devices and boards (headless runner)

[peripherals.md](peripherals.md) gives one end-to-end recipe per device and maps
every UI board to its flags and demo firmware. The flags are:

| flag | attaches |
|---|---|
| `-lcd` | ST7789 240×135 on SPI1 (CS GP9, DC GP8, RST GP12), GEEK wiring |
| `-touch-lcd2` | RP2350-Touch-LCD-2: 240×320 LCD on SPI0, CST816D, QMI8658, battery ADC, microSD |
| `-touch x,y` | inject a tap at native panel coordinates (needs `-touch-lcd2`) |
| `-touch-at D`, `-touch-hold D` | when the tap goes down and how long it lasts (default 150 ms / 100 ms) |
| `-sensors` | print touch, IMU, battery, backlight and SD state at exit |
| `-lcd-png PATH` | write the LCD framebuffer to a PNG at exit (implies `-lcd` unless `-touch-lcd2` is used) |
| `-sd` | microSD on SPI0 (CS GP23), GEEK wiring |
| `-sd-dir PATH` | back the card with a host directory (read/write files from the host) |
| `-sd-image PATH` | raw FAT16 image for `-sd`/`-touch-lcd2` (default: built-in FAT16) |
| `-eth` | W5500 on SPI0 (CS GP17) |
| `-w6300` | W6300 on SPI0 (CS GP17) |
| `-ch9120` | CH9120 on UART1 (GP20/21), CFG0 GP18, RST GP19, TCPCS GP17 |
| `-cyw43439` | CYW43439 gSPI radio on GP23/24/25/29 (see [boards.md](boards.md)) |
| `-cyw43439-ssid NAME` | virtual AP SSID; empty accepts any network |
| `-cyw43439-password PASS` | virtual AP passphrase |
| `-cyw43439-mac ADDR` | station MAC (default `02:11:22:33:44:55`) |
| `-cyw43439-host NAME=IP` | DNS entry for the virtual AP (repeatable) |
| `-cyw43439-no-net` | disable the DHCP/DNS/NAT data path |
| `-ble` | virtual CYW43 BLE controller (Pico W HCI transport; see [ble.md](ble.md)) |
| `-ble-addr ADDR` | advertised BLE peer address (default `C0:DE:12:34:56:78`) |
| `-ble-host-wake GPIO` | CYW43 HOST_WAKE pin (default 24) |
| `-ble-seed N` | BLE controller PRNG seed (default 42) |
| `-ble-hci-log PATH` | write HCI traffic to a pcapng file |
| `-ws2812 GPIO` | decode a WS2812B strip driven on that pin |
| `-otp PATH` | seed the OTP array from a JSON dump before boot |
| `-otp-dump PATH` | write every programmed OTP row to a JSON file at exit |
| `-flash PATH` | load a raw 16 MiB flash image and write it back after self-updates |
| `-flash-out PATH` | write the final flash image to this path without loading one |
| `-psram MIB` | attach PSRAM on QMI CS1 (1, 2, 4, 8 or 16 MiB; 0 disables) |
| `-psram-latency CYCLES` | extra virtual time per PSRAM access (default 4) |
| `-psram-cs GPIO` | QMI CS1 GPIO reported to firmware (default 8) |
| `-rp2350b` | model the RP2350B QFN-80 package (48 GPIOs, PACKAGE_SEL=0) |
| `-dvi-png PATH` | write the last HSTX/DVI frame to a PNG |

`-sd-dir` and `-sd-image` are mutually exclusive. `-eth` and `-w6300` are
mutually exclusive, as are `-lcd` and `-touch-lcd2`.

## Flash and self-update

Flash is a 16 MiB array that starts erased (`0xFF`) and implements page
programming (`1 -> 0`) and 4 KiB sector erase. The Pico SDK's
`flash_range_erase`/`flash_range_program` reach it through the synthetic
bootrom services (`RE`, `RP`; `FO` for `flash_op`). A reset from the bootrom
`reboot` or the watchdog reboots the vector table at `0x10000000`.
`AIRCR.SYSRESETREQ` resets only the requesting core, preserving the other core
and unrelated peripheral state.

- `-flash PATH` loads a raw flash image if it exists and writes the final image
  back after a reset and at exit. Without `-fw`, the runner boots the vector
  table from the image, which is how a previous self-update is resumed.
- `-flash-out PATH` only writes the final image, useful when the input is an
  ELF and you want to compare the programmed bytes.
- A self-update normally writes the new image over the first sectors. The
  updating code must execute from SRAM (as the SDK flash routines do) because
  the running image is erased.
- The flash image is the full 16 MiB array; compare the application prefix with
  the source image, for example:

```sh
size=$(wc -c < build/pico-c/ota2.bin)
dd if=build/ota-flash.bin of=/tmp/prefix.bin bs=1 count="$size" 2>/dev/null
cmp /tmp/prefix.bin build/pico-c/ota2.bin
```

The runner logs each reset (`reason`, `boot`, `entry`) and `flash saved`. The
summary JSON includes `flash_path`, `boots`, `flash_writes`, `reset_reason` and
`boot_entry`. `task c:ota` runs the single-slot demo in `firmware/c/ota`.

This models a **single flat slot** by default. An A/B partition table is also
recognised at the start of a raw flash image (`-flash`): magic `0x50543235`,
two to sixteen 16-byte slot entries with offset/size/flags/version. Boot picks
the highest-version executable slot (or the table's `active` entry), maps its
storage offset to the `0x10000000` XIP window, and translates flash reads
accordingly. `rom_reboot(REBOOT2_FLAG_REBOOT_TYPE_FLASH_UPDATE)` with a base
inside another slot marks it preferred for the next boot. Secure boot, rollback
policy and the picobin/picotool signed table are not implemented.

## PSRAM

QMI chip select 1 is an optional QSPI PSRAM device mapped at `0x11000000`,
with uncached mirrors at `0x15000000` and `0x1d000000`. The `0x19000000`
window is cache maintenance, not data. Attach one with `-psram`:

```sh
rp2350sim -fw firmware/c/build/psram.elf -seconds 2 \
  -psram 8 -psram-cs 8 -psram-latency 6 \
  -expect 'psram: ok' -stop-on-expect
```

- The SDK's `hardware_psram` sees the size and chip select through boot RAM
  `FLASH_DEVINFO`, so `psram_is_available()` and `psram_get_size()` work
  without board-header overrides.
- Window 1 is read-only until firmware sets `XIP_CTRL.WRITABLE_M1`, exactly as
  on hardware. Writes before that are dropped.
- `-psram-latency` is an extra stall added to every PSRAM access (in 150 MHz
  cycles). `0` makes it SRAM-fast; the default 4 models an external QSPI part,
  and `6` is what the `pico2-psram` board uses. The virtual clock, timers and
  DMA see the stall, so `time_us_64()` measures it.
- The QMI block answers the `0x9F` JEDEC ID as an APS6404 (KGD `0x5D`, EID
  `0x26` = 8 MiB) and accepts the SDK's quad-enable/M1 setup. Accesses past the
  configured size are reported as unmapped, like a real bus error.

The C demo `firmware/c/psram` checks the reported size, writes and verifies
1 MiB, and reports the read time so the latency is visible. Use `task c:psram`
or the UI's `pico2-psram` board (`task ui:psram`).

## Network endpoints

The W5500, W6300 and CH9120 models bind their TCP/UDP listeners on the host, so
a server firmware is reachable from this machine even though its guest IP is
emulated. Whenever the network state changes (firmware configuration, listen,
connect, accept, close), the runner logs one line:

```json
{"msg":"network","status":"W6300 ip=10.0.2.15 mask=255.255.255.0 gw=10.0.2.2 mac=02:11:22:33:44:55 tcp/9002 listen host=127.0.0.1:9002 guest=10.0.2.15:9002"}
```

- `host=` is the address to connect to from this machine. A wildcard listener
  is reported as `127.0.0.1:<port>`; the guest IP is not routable from the host.
- `guest=` is the emulated device's `IP:port`, useful when matching firmware
  logs or board documentation.
- `peer=` appears on outbound or accepted connections.
- The CH9120 line includes `mode=tcp-server`, `tcp-client`, `udp-server` or
  `udp-client`; the virtual Wi-Fi transport uses the same module.

The line is logged at info level, so `-log-level warn` suppresses it. The
`-summary-file` JSON includes the same data under `network`:

```json
"network": [{"kind":"W6300","configured":true,"ip":"10.0.2.15",
  "sockets":[{"proto":"tcp","state":"listen","local_port":9002,
  "guest_addr":"10.0.2.15:9002","host_addr":"127.0.0.1:9002"}]}]
```

## OTP

The RP2350 OTP array is modelled as 4096 rows of 24 bits (16 data bits plus
Hamming ECC), with fuse semantics: programming only sets bits and page locks
apply until the next reset. The count is not persistent across runs; seed a
file or dump the result:

```sh
rp2350sim -fw otp.elf -seconds 0.3 -otp-dump otp.json
rp2350sim -fw reader.elf -seconds 0.3 -otp otp.json
```

`firmware/c/otp` expects blank rows on its demo run; after a dump, rerunning
that same demo reports that its rows are no longer blank. Use the dump to seed
firmware that reads the rows instead.

`-otp-dump` writes `{"writes": N, "rows": [{"row": 1024, "raw": "0x096548",
"data": "0x6548", "ecc": "ok"}], "software_locked": [...]}`. `raw` is the
24-bit row including ECC and bit-repair bits, `ecc` is `ok`, `corrected`,
`ecc-error` or `uncorrectable`, and `software_locked` lists pages the firmware
locked through `SW_LOCK`. Only `rows` is read back by `-otp`; a row with `raw`
is restored verbatim (so deliberately corrupted ECC round-trips), a row with
only `data` gets a fresh ECC. `raw` is limited to 24 bits and `data` to 16 bits;
wider values are rejected as load errors.

Firmware reads and writes OTP through the bootrom `otp_access` service, as on
hardware. Pico SDK `firmware/c/otp` exercises ECC, raw rows, fuse rules and
page locking. The OTP tab in the UI shows the pages, locks and rows.

A directory-backed card is packed into FAT16 at startup and written back after
the firmware's writes settle; see [boards.md](boards.md).
The UI has more boards (see [ui.md](ui.md)); the runner flags cover the common
bring-up cases.

A deterministic touch on the Touch-LCD-2:

```sh
rp2350sim -fw touchlcd.elf -seconds 3 -touch-lcd2 \
  -touch 120,200 -touch-at 1s -touch-hold 500ms
```

`-touch` uses native panel coordinates. Add `-sensors` to log the final touch,
IMU acceleration/temperature, battery voltage, PWM backlight and SD card state:

```json
{"msg":"sensors","touch":"120,200","pressed":true,"accel":"0.00,0.00,1.00",
 "temp":25,"sd_blocks":32768,"sd_source":"build/sdcard-host","battery":3.9,
 "backlight":1}
```

The UI-only `pico2-analog` board adds generated waveforms for the scope; every
other UI board maps to the flags above (see
[peripherals.md](peripherals.md#board-to-flag-map)).

### UART over TCP

`-serial-tcp` exposes a hardware UART as a TCP server:

```sh
rp2350sim -fw fw.elf -seconds 30 -serial-tcp 1:127.0.0.1:0
```

The address format is `uart:host:port` (multiple entries comma-separated). Port
`0` asks the kernel for a free port; the assigned address is printed on stderr:

```
msg="uart bridge listening" uart=1 addr=127.0.0.1:39479
```

Connect with any TCP client (`nc`, `socat`, Python) and exchange bytes with the
firmware's UART as if it were wired. Start the client promptly: `-seconds` is
virtual, so the headless process can finish its budget before a person gets
around to connecting. Use the UI's `task ui:serial` for an interactive session.
The listener and client survive firmware resets in the UI; the headless runner
is one process.

### I2C/SPI devices over TCP

`-i2c-tcp` and `-spi-tcp` expose a bus to an external device program. See
[bus-pirate.md](bus-pirate.md) for the token reference and complete fake-device
transcripts. The
emulator is the controller; the TCP peer is the target. The wire format is
Bus Pirate tokens, where `[` is start (or CS low), `]` is stop (or CS high),
and the address byte is `addr << 1`:

```text
< [0xA0      > ACK
< 0x00       > ACK
< [0xA1 r:1  > 0x12
< r:1        > 0x34
< ]          (no response)
```

```text
< [0x9F      > 0xEF
< 0x9F       > 0x40
< 0x9F       > 0x18
< ]          (no response)
```

A compact Bus Pirate command such as `[0xA0 0x00 [0xA1 r:4]` or `[0x9F r:3]`
is the same transaction. The bridge splits it at the controller's synchronous
request boundaries: the firmware needs each ACK/read byte while it runs, so the
peer sees one start/data/read token at a time rather than the whole script.
Reads are answered with one byte per `r:1`; SPI bytes are answered with the
MISO byte. `HELLO` and `]` never expect a response.

```sh
# listen: a script connects
rp2350sim -fw master.elf -seconds 5 \
  -i2c-tcp 0:0x50@127.0.0.1:0 -spi-tcp 0:17@127.0.0.1:0

# connect: the emulator dials a device server (or a second simulator's UART)
rp2350sim -fw master.elf -seconds 5 \
  -i2c-tcp 0:0x50=127.0.0.1:9000 -spi-tcp 0:17=127.0.0.1:9001
```

- `-i2c-tcp bus:addr[,addr...]@host:port` (listen) or `=host:port` (connect).
- `-spi-tcp bus:cs@host:port` or `=host:port`.
- An empty endpoint listens on `127.0.0.1:0`; the assigned address is logged
  as `bus bridge listening`.
- `-bus-timeout` (default 1s) bounds one request. `-bus-wait` (default 2s)
  waits for the first client once; after that a missing client NAKs/returns
  `0xff` immediately.
- One client per bridge. I2C address collisions with board devices and SPI CS
  collisions are rejected. An I2C target can NAK a byte (the controller raises
  `TX_ABRT`); a read NAK aborts the transfer.

### Built-in I2C/SPI presets

Common targets are embedded in the emulator, so a run does not need a second
process or a TCP script:

```sh
rp2350sim -fw build/protocols.elf -seconds 5 \
  -i2c-preset 0:bme280@0x76 -i2c-preset 0:ssd1306@0x3c \
  -i2c-preset 0:mpu6050@0x68 -spi-preset 0:w25q16@17
```

- `-i2c-preset bus:name[@addr]` attaches one built-in target. `addr` selects
  an alternate address and defaults to the first supported one.
- `-spi-preset bus:name@cs` attaches one built-in SPI target on a chip select.
- `-list-presets` prints every preset and its addresses.
- `bme280` (0x76/0x77) exposes chip id 0x60, calibration and a datasheet
  sample; `ssd1306` (0x3c/0x3d) accepts command and data streams and keeps the
  GDDRAM; `mpu6050` (0x68/0x69) reports WHO_AM_I 0x68 and 1 g on Z; `w25q16`
  is a 2 MiB JEDEC SPI flash (EF 40 15) with read, program, erase and write
  enable.
- A preset is attached directly to the controller; it does not listen on TCP.
  It cannot share an I2C address or SPI chip select with another device.
- `firmware/presets` probes all four models; `task run:presets` builds and runs
  it and prints `bme280: 60`, `mpu6050: 68`, `ssd1306: ok` and
  `w25q16: ef 40 15`.

`tools/busfake` is a ready-made host device (EEPROM at 0x50, JEDEC flash on
the CS pin). It connects to a listening bridge:

```sh
busfake -kind i2c -addr 127.0.0.1:39479
busfake -kind spi -addr 127.0.0.1:39480
```

The repository also has `firmware/busmaster` (I2C read + SPI JEDEC ID) and
`firmware/busdevice` (a TinyGo software device on two UARTs). Run the master
with connect-mode bridges pointed at the device simulator's UART bridges to
exercise two emulator instances:

```sh
rp2350sim -fw build/busdevice.elf -seconds 300 \
  -serial-tcp 0:127.0.0.1:0,1:127.0.0.1:0
rp2350sim -fw build/busmaster.elf -seconds 5 \
  -i2c-tcp 0:0x50=127.0.0.1:<UART0> -spi-tcp 0:17=127.0.0.1:<UART1>
```

## Diagnostics and debugging

| flag | meaning |
|---|---|
| `-trace N` | trace the first N instructions (stderr) |
| `-trace-from ADDR` | start tracing at this address |
| `-itm` | print Cortex-M33 ITM stimulus writes to stdout |
| `-trace-last` | keep a ring of recent instructions for faults |
| `-trace-pins 25,15` | log GPIO level changes |
| `-bp ADDR\|SYMBOL`, `-bp2 ADDR\|SYMBOL` | log at an address or ELF symbol (requires ELF symbols for names) |
| `-watch ADDR\|SYMBOL`, `-watch-val V`, `-watch-reg N` | log writes at an address/symbol / value / register changes |
| `-poke ADDR=VAL` | write memory after boot |
| `-peek ADDR` | print a word at exit |
| `-sample N` | print the PC in 10M-cycle chunks (obeys input, expectations and the wall timeout) |
| `-continue-after-halt` | keep peripherals (PIO/PWM/DMA) running after a core halts |
| `-hang-cycles N` | report a tight poll loop after N cycles (default 30M, 0 disables) |
| `-profile` | collect PC samples and exact instruction coverage |
| `-profile-interval N` | PC sampling period in cycles (default 4096) |
| `-coverage PATH` | write an LCOV tracefile at exit |
| `-profile-out PATH` | write the profile report as JSON at exit |
| `-profile-top N` | log the N hottest functions at exit |
| `-strict-mmio` | halt on the first unmapped peripheral access |
| `-unhandled` | report unmapped accesses at exit |
| `-msg` | at `-bp`, decode `r1` as a runtime message string |
| `-cpuprofile PATH` | write a Go CPU profile |
| `-gdb ADDR` | serve the GDB remote protocol on `ADDR` (e.g. `127.0.0.1:3333`) |
| `-version` | print the version and exit |
| `-logic-pins 0,1,4,5` | sample these GPIOs into the logic analyzer trace |
| `-logic-out PATH` | write the trace file (default `logic-analyze-trace-<date>-<board>.txt`) |
| `-logic-uart tx[,rx],baud` | decode a UART pin trace |
| `-logic-i2c sda,scl` | decode an I2C pin trace |
| `-logic-spi sck,mosi[,miso[,cs]]` | decode an SPI pin trace |
| `-logic-rate HZ` | sample rate (default 2 MHz) |
| `-logic-depth N` | ring depth in samples (default 16384) |
| `-logic-board NAME` | board label for the file name and metadata (default: the firmware file name) |
| `-vcd PATH` | write the captured pins as an IEEE 1364 VCD (GTKWave/PulseView) |
| `-i2c-tcp bus:addr[,addr][@listen|=connect]` | expose I2C targets over TCP (repeatable) |
| `-spi-tcp bus:cs[@listen|=connect]` | expose an SPI device over TCP (repeatable) |
| `-i2c-preset bus:name[@addr]` | attach a built-in I2C device (repeatable) |
| `-i2c-plugin bus:addr=path` | attach a Lua/WASM I2C device (repeatable) |
| `-spi-plugin bus:cs=path` | attach a Lua/WASM SPI device (repeatable) |
| `-uart-plugin uart=path` | attach a Lua/WASM UART device (repeatable) |
| `-spi-preset bus:name@cs` | attach a built-in SPI device (repeatable) |
| `-onewire PIN[:TEMP]` | attach a DS18B20 on a GPIO (repeatable) |
| `-dht PIN[:KIND[:HUM[:TEMP]]]` | attach a DHT11/22 (repeatable) |
| `-ultrasonic TRIG:ECHO[:CM]` | attach an HC-SR04 (repeatable) |
| `-gps UART[:LAT:LON]` | attach an NMEA GPS source |
| `-can CS[:INT[:PEER_ID]]` | attach an MCP2515 and a CAN peer |
| `-can-bus 0\|1` | SPI controller for `-can` (default 0) |
| `-list-presets` | print the built-in device registry |
| `-bus-timeout D` | I2C/SPI bridge request deadline (default 1s) |
| `-bus-wait D` | first-client wait for an I2C/SPI bridge (default 2s) |

`-i2c-plugin`, `-spi-plugin` and `-uart-plugin` take a `.lua` or `.wasm`
device file. Lua runs in the bundled wasm interpreter and both formats use the
same ABI; `print`/`host_log` lines are emitted as `plugin` records. See
[device plugins](plugins.md) for the schema and examples.

`-watch-reg` installs its own trace callback and works without `-bp` or
`-trace-last`. Address zero is a valid breakpoint; the runner tracks whether
`-bp`/`-bp2` were set rather than using zero as a sentinel. `-strict-mmio`
keeps the instruction ring even without `-trace-last`, so the first unmapped
access gets a backtrace. Malformed `-trace-from`, `-poke`, `-watch-val` and
`-peek` values are rejected before the machine starts (exit 2).

### Profiling and coverage

`-profile` enables two views at once: a PC sample every `-profile-interval`
cycles (default 4096, about 27 us) and an exact count of every executed
instruction. Sampling attributes virtual cycles to functions; exact counts
produce instruction and line coverage plus LCOV.

```sh
rp2350sim -fw build/blinky.elf -seconds 1 \
  -profile -coverage build/blinky.info \
  -profile-out build/blinky-profile.json -profile-top 10
```

- `-coverage PATH` writes LCOV (`SF`/`FN`/`FNDA`/`DA`/`LF`/`LH`), using DWARF
  statement rows as the line set and instruction hits as the counts. It needs
  an ELF with debug info; UF2/raw firmware still samples.
- `-profile-out PATH` writes the same report as JSON: per-function samples,
  cycles, instruction totals and hits, line counts, the IRQ latency histogram
  and stack low-water marks.
- The exit log line `profile` reports virtual time, samples, instruction
  coverage, IRQ entries and the slowest latency; `-profile-top` adds one line
  per hot function.
- IRQ latency is measured from a peripheral/software IRQ becoming pending to
  its handler entry (nested waits included), in 32-cycle to 131072-cycle
  buckets. Stack low-water marks and latency are sampled with the PC, so they
  are approximations; instruction coverage is exact.
- Profiling uses virtual time only: the same firmware and run length produce
  the same report. See [profiler.md](profiler.md) for the data model, LCOV
  semantics and the browser fallback.

### Logic analyzer capture

The runner can save the same trace the UI's logic tab shows. Hardware
UART/SPI/I2C taps are always included; `-logic-pins` adds raw pin samples and
the protocol flags decode those pins for PIO/bit-banged traffic. Protocol pin
numbers must be GPIOs 0-47 (optional SPI MISO/CS may be `-1`); malformed lists
are usage errors. UART pin decoding drops a frame if its data or stop-bit
samples are missing.

```sh
rp2350sim -fw build/protocols.elf -seconds 0.5 \
  -logic-pins 0,1,4,5,18,19,16,17 \
  -logic-uart 0,1,115200 -logic-i2c 4,5 -logic-spi 18,19,16,17 \
  -logic-out logic-trace.txt
```

`-continue-after-halt` is for examples that start a PIO or PWM engine from
`main` and then return: the cores stop, but the peripheral clock keeps running
so the captured waveform is still valid. It implies `-allow-halt`.

Without `-logic-out` the file is written to the current directory as
`logic-analyze-trace-<date>-<board>.txt`. The text format has a metadata block,
a `# samples` table (`time_us` plus one column per GPIO), and decoded sections
for the controller taps and pin protocols. A pin trace is a rolling window of
`-logic-depth` samples; lower `-logic-rate` to cover more time. Capturing pins
installs a periodic sampler on the requested grid, which keeps the idle
fast-forward available. The UI's **Save trace** writes the same format plus a
USB CDC section.

`-vcd PATH` writes the same pin window as an IEEE 1364 value change dump
(timescale 1 ns, one wire per GPIO), ready for GTKWave or PulseView. It needs
at least one captured pin, from `-logic-pins` or the pins named by
`-logic-uart`/`-logic-i2c`/`-logic-spi`:

```sh
rp2350sim -fw build/protocols.elf -seconds 0.2 \
  -logic-uart 0,1,115200 -logic-i2c 4,5 -vcd trace.vcd
```

### Audio capture

The runner decodes one audio source from GPIO pins and writes a WAV file when
the run ends. Exactly one source flag is required, plus `-audio-out`.

| flag | meaning |
|---|---|
| `-audio-out PATH` | write the decoded capture as 16-bit PCM WAV |
| `-audio-i2s bclk,lrclk,data[,bits]` | Philips I2S, `bits` 16/24/32 (default 16) |
| `-audio-pdm data,clock[,decimation]` | PDM microphone bitstream, density averaged (default 64 clocks per sample) |
| `-audio-pwm PIN` | PWM or plain GPIO pin: average the duty cycle over the sample period |
| `-audio-rate HZ` | output sample rate (default 48000) |

```sh
rp2350sim -fw build/audio.elf -seconds 1 -audio-pwm 0 -audio-out tone.wav
tools/wavcheck -rate 48000 -channels 1 -freq 440 -min-rms 0.1 tone.wav
```

The PWM/GPIO mode is the useful one for TinyGo programs without an audio
codec: it measures the pin's high time over one output sample period, so a
square wave becomes a square wave and a hardware PWM carrier does not alias.
I2S and PDM are decoded from edge streams and are covered by synthetic tests;
`firmware/audio` is the PWM example. The capture is capped at ten minutes to
bound memory.

A decoded GPIO square wave is full scale (0 dBFS), and a pin parked high
decodes as DC. The saved WAV is the raw capture, so attenuate it and remove
any DC offset before playing it on a host.

### MIDI

USB-MIDI and UART-MIDI can be monitored, bridged to UDP-MIDI and driven from
the host.

| flag | meaning |
|---|---|
| `-usb-midi` | expose the USB-MIDI class on endpoint 3 (TinyGo `machine/usb/adc/midi`) |
| `-midi-uart N` | tap UART `N` as 31250-baud MIDI |
| `-midi-monitor` | log every parsed message |
| `-midi-udp ADDR` | bidirectional UDP-MIDI endpoint; replies go to the last sender |

```sh
rp2350sim -fw build/midi.elf -usb-midi -midi-monitor -midi-udp 127.0.0.1:5004
```

A datagram with raw MIDI bytes (running status included) is parsed and
delivered to the firmware: over USB-MIDI as event packets on endpoint 3, or
over the UART receive FIFO. Firmware messages are logged and sent back to the
last UDP sender. The UART tap watches the firmware's TX line only; an echo
firmware cannot loop back into itself. USB-MIDI requires a composite
CDC+MIDI descriptor (`descriptor.CDCMIDI`); the emulated host enumerates it
like the CDC-only descriptor.

### Environment variables

Each legacy variable enables one debug stream without changing the level of
the others. Setting `-log-level debug` enables all of them.

| variable | effect |
|---|---|
| `RP2350SIM_ETH_LOG` | W5500 socket activity |
| `RP2350SIM_W6300_LOG` | W6300 socket activity |
| `RP2350SIM_CH9120_LOG` | CH9120 configuration and socket transfers (tx/rx sizes) |
| `RP2350SIM_SPI_LOG` | every SPI byte |
| `RP2350SIM_DMA_LOG` | DMA transfers |
| `RP2350SIM_LCD_LOG` | ST7789 command decoding |
| `RP2350SIM_USB_LOG` | USB host/device state |
| `RP2350SIM_PIO_LOG` | PIO state machine execution |
| `RP2350SIM_I2C_LOG` | every I2C address, byte and STOP |
| `RP2350SIM_DCP_LOG` | RP2350 double coprocessor operations (operands and result) |

## Examples

```sh
# 3 virtual seconds, firmware console only
rp2350sim -fw firmware.elf -seconds 3 > firmware.log

# Interactive console, fail unless it answers the command
printf 'status\n' | rp2350sim -fw firmware.elf -seconds 3 -stdin \
  -expect 'status: ok' -stop-on-expect -quiet

# Assert multiple pieces of output and keep a machine-readable summary
rp2350sim -fw firmware.elf -seconds 4 -quiet \
  -expect 'wifi: connected' -expect 'tcp: ok' \
  -summary-file summary.json

# Trace a pin for three seconds
rp2350sim -fw firmware.elf -seconds 3 -trace-pins 25

# Export the LCD framebuffer
rp2350sim -fw firmware.elf -seconds 3 -lcd -lcd-png lcd.png

# Human-readable logs in a terminal, machine-readable in a pipe
rp2350sim -fw firmware.elf -seconds 3 -log-format text
rp2350sim -fw firmware.elf -seconds 3 -log-format json 2> run.jsonl
```

For interactive breakpoints, registers, memory and variables, see
[debug.md](debug.md). `-bp` and `-watch` accept symbol names when `-fw` is an
ELF; UF2/raw images need numeric addresses.

See [ci.md](ci.md) for CI recipes and [tinygo.md](tinygo.md) for build
integration.
