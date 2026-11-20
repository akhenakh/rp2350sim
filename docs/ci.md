# Running firmware in CI

The headless runner is designed to be scripted: firmware output on stdout,
diagnostics on stderr, meaningful exit codes, streaming expectations, timed
input, and a JSON summary.

## Minimal job

```sh
set -eu

# TinyGo example; replace this build step with your Pico SDK C CMake build.
tinygo build -target=pico2 -o firmware.elf .

rp2350sim -fw firmware.elf -seconds 3 -quiet \
  -expect "boot: ok" \
  -stop-on-expect \
  -summary-file summary.json \
  > firmware.log
```

- `-quiet` keeps the informational header and summary records out of the way
  (they are on stderr anyway).
- `-expect` fails with exit code **4** if the string never appears.
- `-stop-on-expect` ends the run as soon as all expectations match, so a fast
  firmware does not wait for the whole virtual budget.
- `firmware.log` contains only what the firmware printed.
- `summary.json` records cycles, PC/SP, halt state and the exit code.
- stderr is JSON (one `slog` object per line) because a CI run is not a
  terminal; `-log-format text` opts back into `key=value` for a person.

Pico SDK C projects keep the same `rp2350sim` invocation after their
`cmake --build`; only the build step differs. The GitHub Actions example below
installs TinyGo; a C job installs the ARM toolchain and `PICO_SDK_PATH` instead.

## Interacting with the firmware

Most firmware reads commands from the USB CDC console. Pipe a script:

```sh
printf 'status\nhelp\n' | rp2350sim -fw firmware.elf -seconds 3 -stdin \
  -expect "status: ok" -stop-on-expect -quiet > firmware.log
```

For a command that must arrive after a slow boot:

```sh
rp2350sim -fw firmware.elf -seconds 3 \
  -input 'status\n' -input-at 800ms \
  -expect "status: ok" -quiet
```

`-input-file` sends a file; `-input` and `-input-file` are concatenated. Input
goes to both USB CDC and UART0, and USB delivery waits for enumeration.

For a serial protocol (or a firmware whose console is UART1), use a TCP bridge:

```sh
rp2350sim -fw firmware.elf -seconds 10 -serial-tcp 1:127.0.0.1:0 \
  2> emulator.jsonl > firmware.log &
EMU_PID=$!
ADDR=$(sed -n 's/.*"msg":"uart bridge listening".*"addr":"\([^"]*\)".*/\1/p' emulator.jsonl | head -1)
printf 'PING\n' | nc "${ADDR%:*}" "${ADDR##*:}"
wait $EMU_PID
```

`jq -r 'select(.msg=="uart bridge listening") | .addr' emulator.jsonl` is the
equivalent when `jq` is available.

This is also how you drive the CH9120 / W5500 / W6300 / virtual Wi-Fi demos:
their sockets are backed by the host's network stack, so start the server the
firmware expects first (for example `go build -o build/ethhost ./tools/ethhost` then `build/ethhost`).

## Pico SDK examples and signal checks

The SDK builds `pico_cxx_options` even for C-only programs, so the ARM
toolchain needs its C++ headers. On Ubuntu install
`libstdc++-arm-none-eabi-newlib` (the headers come with its `-dev` dependency);
`--no-install-recommends` alone leaves `<cassert>` missing.

`tools/picoexamples.sh` downloads a pinned `raspberrypi/pico-examples` snapshot
(SDK 2.3.1, commit `0d62f75`) and builds the subset the emulator supports:
hello world over UART, blink, UART, timer, multicore, DMA, PIO, PWM, ADC,
reset, watchdog, I2C bus scan, divider and DCP. FreeRTOS and the examples that
need external hardware are not built. The `task ci` smoke suite runs each one
and checks its console output; GPIO-producing examples are checked on the wire,
not just on stdout.

`tools/pincheck` measures a pin in a logic trace produced by `-logic-pins`:

```sh
rp2350sim -fw pwm.elf -seconds 0.01 -continue-after-halt \
  -logic-pins 0,1 -logic-rate 150e6 -logic-out pwm.txt
pincheck -pin 0 -freq 37.5e6 -duty 25 pwm.txt
pincheck -pin 1 -freq 37.5e6 -duty 75 pwm.txt
```

`-freq` has a 5% default tolerance (`-freq-tol`), `-duty` is in percentage
points (`-duty-tol`), and `-min-edges`/`-max-edges` bound the transition count.
This is how CI validates the PIO square wave (15 MHz, 50%), blink (2 Hz),
the PIO blink frequencies and the PWM duty cycles. Build the subset on its own
with `task picoexamples:build` (or `bash tools/picoexamples.sh build/pico-examples`).

## Pull request reports

The `ci` workflow runs on every pull request. It captures the full `task ci`
output to `ci.log`, uploads it as the `ci-log` artifact, and a separate
`report` job updates one PR comment (marked with
`<!-- rp2350sim-smoke-report -->`) with:

- the passed/failed counts and the CI job result;
- a link to the workflow run and the head commit;
- the failed checks;
- a collapsed table of every smoke check.

The comment is updated in place on each push, so a PR keeps a single report.
The test job only has `contents: read`; the write token lives in the `report`
job, which never executes PR code. Pull requests from forks are skipped by the
report step because GitHub gives `pull_request` runs from forks a read-only
token; their checks still run and show in the PR's checks tab.

## I2C/SPI device bridges

The `I2C/SPI TCP bridge` smoke checks use the Bus Pirate token protocol from
`docs/cli.md`. Two paths are covered:

1. A host device (`tools/busfake`) connects to a listening bridge on a
   `busmaster` firmware. The firmware reads two EEPROM bytes and a JEDEC ID;
   the test asserts `i2c: 12 34` and `spi: ef 40 18`.
2. Two simulators: `firmware/busdevice` answers on UART0/UART1, and
   `firmware/busmaster` uses connect-mode bridges (`=UART address`) to dial the
   second simulator's UART bridges. No relay is involved.

```sh
# second simulator: software device on UART0/UART1
rp2350sim -fw build/busdevice.elf -seconds 300 \
  -serial-tcp 0:127.0.0.1:0,1:127.0.0.1:0 2> busdevice.log &

# first simulator: Bus Pirate controller
rp2350sim -fw build/busmaster.elf -seconds 5 \
  -i2c-tcp "0:0x50=127.0.0.1:<UART0>" \
  -spi-tcp "0:17=127.0.0.1:<UART1>" \
  -expect 'i2c: 12 34' -expect 'spi: ef 40 18' -stop-on-expect
```

Parse the `uart bridge listening` records from `busdevice.log` for the two
ports. The bridges wait for their first client (`-bus-wait`), so starting the
controller just after the device is enough; a missing client NAKs after the
timeout instead of hanging forever.

## Exit codes

| code | action |
|---|---|
| 0 | success |
| 1 | load/config error |
| 2 | usage error |
| 3 | firmware halt/fault (unimplemented instruction, unmapped MMIO, …) |
| 4 | a required `-expect` string was missing |
| 5 | `-wall-timeout` expired |

A halted core exits **3**; use `-allow-halt` only when a test intentionally
faults the CPU.

## Wall-clock budgets

Virtual time and wall time are different. The emulator usually runs faster than
the 150 MHz emulated clock, but a heavy firmware or host network can be slower.
`-wall-timeout` bounds real time:

```sh
rp2350sim -fw firmware.elf -seconds 600 -wall-timeout 30s -quiet
```

The process then exits 5 instead of hanging a runner.

## Artifacts

Collect `firmware.log`, `summary.json`, and optional screenshots:

```sh
rp2350sim -fw firmware.elf -seconds 3 -lcd -lcd-png lcd.png \
  -summary-file summary.json > firmware.log
```

The UI can render a headless snapshot too (see [ui.md](ui.md)):

```sh
rp2350sim-ui --board geek --tab lcd --run 0.6 \
  --png ui.png firmware.elf
```

## GitHub Actions

```yaml
name: firmware
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Install TinyGo
        run: |
          version=0.43.0
          curl -fsSL "https://github.com/tinygo-org/tinygo/releases/download/v${version}/tinygo${version}.linux-amd64.tar.gz" \
            | sudo tar -xz -C /opt
          echo "/opt/tinygo/bin" >> "$GITHUB_PATH"

      - name: Install rp2350sim
        run: |
          go install github.com/akhenakh/rp2350sim/cmd/rp2350sim@latest
          echo "$(go env GOPATH)/bin" >> "$GITHUB_PATH"

      - name: Run firmware assertions
        run: |
          tinygo build -target=pico2 -o firmware.elf .
          printf 'status\n' | rp2350sim -fw firmware.elf -seconds 3 -stdin \
            -expect "status: ok" -stop-on-expect -quiet \
            -summary-file summary.json > firmware.log

      - uses: actions/upload-artifact@v4
        if: always()
        with:
          name: firmware-run
          path: |
            firmware.log
            summary.json
```

If the project lives in the same repository as the emulator, `go install
./cmd/rp2350sim` from the root is quicker and avoids a network fetch.

## Drop-in Taskfile / Makefile

`tools/Taskfile.pico2sim.yml` defines `build`, `run`, and `ci`; include it as
`pico2sim` and run `task pico2sim:ci EXPECT="status: ok"`. `tools/pico2sim.mk`
provides `pico2-build`, `pico2-run` and `pico2-ci` for Make users. See
[tinygo.md](tinygo.md).

## Logs as an artifact

The JSON log is the record of what the machine did, not just a side channel:

```sh
jq -r 'select(.level=="ERROR") | .msg' emulator.jsonl        # what failed
jq -r 'select(.msg=="run finished") | .cycles' emulator.jsonl # work done
jq -r 'select(.msg=="trace") | .pc' emulator.jsonl | head    # decoder work
```

`-log-level debug` adds the per-instruction trace, PIO, USB and socket
streams; the legacy `RP2350SIM_*_LOG` variables enable a single stream.

## Pitfalls

- **Expectations match the firmware console only.** Peripheral/emulator logs
  are on stderr and are not searched.
- **USB input needs enumeration.** `-input-at` defaults to 200 ms; raise it if
  the firmware has a long boot. `-stdin` bytes wait in a queue and are flushed
  once USB is ready.
- **Random ports:** `-serial-tcp 1:127.0.0.1:0` logs the assigned address as the
  `addr` of the `uart bridge listening` record; parse it rather than
  hardcoding a port.
- **Network timings are wall-clock.** DHCP-less emulated modules connect
  immediately, but host servers must be listening first. A TCP dial failure is
  reported in the device log on stderr, not as a process failure unless you
  assert on firmware output.
- **Halts are failures.** If a CPU fix is in progress, `-allow-halt` keeps the
  exit code 0 while you inspect the trace.
