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
| `2` | usage error (unknown flag, missing `-fw`, conflicting device flags) |
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

`-input`/`-input-file` are useful for firmware that waits for a command after
boot; `-stdin` is for scripted sessions:

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
rp2350sim -fw fw.elf -seconds 5 -input 'status\n' \
  -expect 'status: ok' -stop-on-expect -quiet > fw.log
```

## Devices and boards (headless runner)

| flag | attaches |
|---|---|
| `-lcd` | ST7789 240×135 on SPI1 (CS GP9, DC GP8, RST GP12), GEEK wiring |
| `-touch-lcd2` | RP2350-Touch-LCD-2: 240×320 LCD on SPI0, CST816D, QMI8658, battery ADC, microSD |
| `-touch x,y` | inject a tap at native panel coordinates (needs `-touch-lcd2`) |
| `-touch-at D`, `-touch-hold D` | when the tap goes down and how long it lasts (default 150 ms / 100 ms) |
| `-sensors` | print touch, IMU, battery, backlight and SD state at exit |
| `-lcd-png PATH` | write the LCD framebuffer to a PNG at exit |
| `-sd` | microSD on SPI0 (CS GP23), GEEK wiring |
| `-sd-dir PATH` | back the card with a host directory (read/write files from the host) |
| `-sd-image PATH` | raw FAT16 image for `-sd`/`-touch-lcd2` (default: built-in FAT16) |
| `-eth` | W5500 on SPI0 (CS GP17) |
| `-w6300` | W6300 on SPI0 (CS GP17) |
| `-ch9120` | CH9120 on UART1 (GP20/21), CFG0 GP18, RST GP19, TCPCS GP17 |
| `-ws2812 GPIO` | decode a WS2812B strip driven on that pin |
| `-otp PATH` | seed the OTP array from a JSON dump before boot |
| `-otp-dump PATH` | write every programmed OTP row to a JSON file at exit |
| `-flash PATH` | load a raw 16 MiB flash image and write it back after self-updates |
| `-flash-out PATH` | write the final flash image to this path without loading one |

`-sd-dir` and `-sd-image` are mutually exclusive. `-eth` and `-w6300` are
mutually exclusive, as are `-lcd` and `-touch-lcd2`.

## Flash and self-update

Flash is a 16 MiB array that starts erased (`0xFF`) and implements page
programming (`1 -> 0`) and 4 KiB sector erase. The Pico SDK's
`flash_range_erase`/`flash_range_program` reach it through the synthetic
bootrom services (`RE`, `RP`; `FO` for `flash_op`). A reset from the bootrom
`reboot`, `AIRCR.SYSRESETREQ`, or the watchdog reboots the vector table at
`0x10000000`.

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

This models a **single flat slot**. A/B slots, rollback and secure-boot
validation are not implemented; the loader still requires a vector table at
`0x10000000`.

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
rp2350sim -fw otp.elf -seconds 0.3 -otp otp.json
```

`-otp-dump` writes `{"writes": N, "rows": [{"row": 1024, "raw": "0x096548",
"data": "0x6548", "ecc": "ok"}], "software_locked": [...]}`. `raw` is the
24-bit row including ECC and bit-repair bits, `ecc` is `ok`, `corrected`,
`ecc-error` or `uncorrectable`, and `software_locked` lists pages the firmware
locked through `SW_LOCK`. Only `rows` is read back by `-otp`; a row with `raw`
is restored verbatim (so deliberately corrupted ECC round-trips), a row with
only `data` gets a fresh ECC.

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

### UART over TCP

`-serial-tcp` exposes a hardware UART as a TCP server:

```sh
rp2350sim -fw fw.elf -seconds 30 -serial-tcp 1:127.0.0.1:0
```

The address format is `uart:host:port` (multiple entries comma-separated). Port
`0` asks the kernel for a free port; the assigned address is printed on stderr:

```
uart bridge: uart1 listening on 127.0.0.1:39479
```

Connect with any TCP client (`nc`, `socat`, Python) and exchange bytes with the
firmware's UART as if it were wired. The listener and client survive firmware
resets in the UI but the headless runner is one process.

## Diagnostics and debugging

| flag | meaning |
|---|---|
| `-trace N` | trace the first N instructions (stderr) |
| `-trace-from ADDR` | start tracing at this address |
| `-trace-last` | keep a ring of recent instructions for faults |
| `-trace-pins 25,15` | log GPIO level changes |
| `-bp ADDR`, `-bp2 ADDR` | stop/log at an address (hex) |
| `-watch ADDR`, `-watch-val V`, `-watch-reg N` | log writes / register changes |
| `-poke ADDR=VAL` | write memory after boot |
| `-peek ADDR` | print a word at exit |
| `-sample N` | print the PC in 10M-cycle chunks |
| `-strict-mmio` | halt on the first unmapped peripheral access |
| `-unhandled` | report unmapped accesses at exit |
| `-msg` | at `-bp`, decode `r1` as a runtime message string |
| `-cpuprofile PATH` | write a Go CPU profile |

### Environment variables

| variable | effect |
|---|---|
Each legacy variable below enables one debug stream without changing the level
of the others. Setting `-log-level debug` enables all of them.

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

See [ci.md](ci.md) for CI recipes and [tinygo.md](tinygo.md) for build
integration.
