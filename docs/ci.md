# Running firmware in CI

The headless runner is designed to be scripted: firmware output on stdout,
diagnostics on stderr, meaningful exit codes, streaming expectations, timed
input, and a JSON summary.

## Minimal job

```sh
set -eu

tinygo build -target=pico2 -o firmware.elf .

rp2350sim -fw firmware.elf -seconds 3 -quiet \
  -expect "boot: ok" \
  -stop-on-expect \
  -summary-file summary.json \
  > firmware.log
```

- `-quiet` keeps the emulator header and summary out of the way (they are on
  stderr anyway).
- `-expect` fails with exit code **4** if the string never appears.
- `-stop-on-expect` ends the run as soon as all expectations match, so a fast
  firmware does not wait for the whole virtual budget.
- `firmware.log` contains only what the firmware printed.
- `summary.json` records cycles, PC/SP, halt state and the exit code.

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
  2> emulator.log > firmware.log &
EMU_PID=$!
PORT=$(sed -n 's/.*uart1 listening on [^:]*:\([0-9]*\).*/\1/p' emulator.log | head -1)
printf 'PING\n' | nc 127.0.0.1 "$PORT"
wait $EMU_PID
```

This is also how you drive the CH9120 / W5500 / W6300 / virtual Wi-Fi demos:
their sockets are backed by the host's network stack, so start the server the
firmware expects first (for example `go run ./tools/ethhost`).

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
(cd ui && go run . --board geek --tab lcd --run 0.6 \
  --png ../ui.png ../firmware.elf)
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

## Pitfalls

- **Expectations match the firmware console only.** Peripheral/emulator logs
  are on stderr and are not searched.
- **USB input needs enumeration.** `-input-at` defaults to 200 ms; raise it if
  the firmware has a long boot. `-stdin` bytes wait in a queue and are flushed
  once USB is ready.
- **Random ports:** `-serial-tcp 1:127.0.0.1:0` prints the assigned address on
  stderr; parse it rather than hardcoding a port.
- **Network timings are wall-clock.** DHCP-less emulated modules connect
  immediately, but host servers must be listening first. A TCP dial failure is
  reported in the device log on stderr, not as a process failure unless you
  assert on firmware output.
- **Halts are failures.** If a CPU fix is in progress, `-allow-halt` keeps the
  exit code 0 while you inspect the trace.
