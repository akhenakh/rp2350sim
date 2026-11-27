# Releases

Releases are built with [GoReleaser](https://goreleaser.com) from the tag
workflow. They are published on the
[GitHub Releases page](https://github.com/akhenakh/rp2350sim/releases). Pushing
a `v*` tag (or dispatching the `release` workflow with a tag) publishes a
GitHub release with both programs:

| artifact | contents |
|---|---|
| `rp2350sim_<version>_linux_<arch>.tar.gz` | `rp2350sim`, `rp2350sim-ui`, LICENSE, README, docs |
| `rp2350sim_<version>_darwin_<arch>.tar.gz` | same |
| `rp2350sim_<version>_windows_<arch>.zip` | `rp2350sim.exe`, `rp2350sim-ui.exe`, LICENSE, README, docs |
| `rp2350sim_<version>_checksums.txt` | SHA-256 checksums |

Platforms are `amd64` and `arm64` for Linux, macOS and Windows. `rp2350sim` is
the headless runner; `rp2350sim-ui` is the Shirei desktop UI. Both report their
version with `-version`.

The binaries are pure Go (`CGO_ENABLED=0`), including the UI's Shirei/purego
backend, so a single Linux runner cross-compiles every target.

## GitHub paths

Replace `<tag>` with the release tag (for example `v0.5.0`) and `<previous-tag>`
with the prior tag. Keep the paths themselves abstract:

- repository: `https://github.com/akhenakh/rp2350sim`
- releases: `https://github.com/akhenakh/rp2350sim/releases`
- tag: `https://github.com/akhenakh/rp2350sim/releases/tag/<tag>`
- asset: `https://github.com/akhenakh/rp2350sim/releases/download/<tag>/<asset>`
- source archive: `https://github.com/akhenakh/rp2350sim/archive/refs/tags/<tag>.tar.gz`
- comparison: `https://github.com/akhenakh/rp2350sim/compare/<previous-tag>...<tag>`

## Release sequence

1. Update this document and the user-facing docs, then commit them. Move the
   work being released from **Next release** into `v<version> highlights`, and
   keep **Next release** for work that follows the tag.
2. Tag that documentation commit as `v<version>` and push the tag.
3. The release workflow builds, attests and publishes the archives.
4. Update the Homebrew tap and AUR package from the published checksums.

Committing the release notes before tagging keeps the tag, source archive and
release page consistent without retroactively editing the tagged commit.

## Next release (unreleased)

- `task ci` runs lint, the unit tests and the smoke suite concurrently. Lint is
  split per module, the tests run as independent tasks, and the smoke suite is
  nine slices (`basic1`, `network`, `basic2`, `c`, `pex1`–`pex4`): the network
  checks stay together because they share host ports, and the Pico SDK C
  examples and pico-examples are built once and reused. Each slice writes its
  own `build/smoke-<slice>.log`. On a 24-core host the warm full run drops from
  2:18 to 36 s at about 4× CPU; `bash tools/smoke.sh` still runs the whole
  suite sequentially.
- The cores scheduler affinity API is covered: `firmware/assigncore` pins one
  goroutine to each core with `machine.AssignCore` and verifies `CurrentCore`
  after sleeps and channel wakeups. `task run:assigncore` runs it, and the
  smoke suite asserts both cores report `ok=true`.

## v0.5 highlights

v0.5 turns the emulator into a sensor and protocol lab: scripted and timed
devices, a CAN controller, NMEA GPS, ITM/DWT trace and a matching desktop UI.
See the [What's New #5](videos.md#whats-new-5) tour.

- Scripted device plugins: I2C, SPI and UART models can be shipped as Lua or
  WASM files and loaded from `boards.yaml`, the CLI or the Bus tab. Lua runs
  in a bundled wasm interpreter and both formats share one ABI; desktop and
  headless runs use wazero, while the browser uses native WebAssembly. Plugin
  `print`/`host_log` lines are shown in the Bus tab.
- Timed wire devices: a DS18B20 on 1-Wire, DHT11/DHT22 and HC-SR04 models use
  cycle-exact edge deadlines rather than scripted bus transactions. A
  deterministic NMEA GPS source, a classic CAN 2.0B bus with a responding peer
  and an MCP2515 SPI controller join them. `-onewire`, `-dht`, `-ultrasonic`,
  `-gps` and `-can` attach them headlessly; `pico2-lcd-lab` and
  `firmware/lab` show all of them on one LCD dashboard.
- Cortex-M33 ITM stimulus ports and the DWT cycle/PC counters are modelled,
  including byte and halfword ITM accesses. The Trace tab groups ITM writes
  into lines, and `-itm` prints them headlessly. The GPS, CAN and Trace tabs
  have `ui:gps`, `ui:can` and `ui:trace` tasks.
- Plugin hardening: desktop calls have a 50 ms deadline (500 ms during init),
  a 64 MiB memory cap, a shared wazero compilation cache and deterministic
  Lua clock/time. Lua exposes no `os`/`io`/`debug`, config encoding rejects
  non-finite/cyclic values, and the WASM fixtures use a real static allocator.
  UART plugins own a UART; I2C/SPI plugins cannot inject serial bytes. Board
  plugin pins and the new device pins are validated against package bounds and
  shared GPIOs.
- ITM/DWT registers are per-core, CYCCNT supports write-to-reset and
  CTRL.CYCCNTENA, exact ITM write widths are preserved, and trace LAR/LSR
  accesses are no-ops. DHT22 negatives use sign-magnitude, DHT11 and HC-SR04
  timings are bounded, MCP2515 READ/RX STATUS and interrupt deassert are
  corrected, and extended CAN IDs are covered. Device models reset with the
  chip through a reset hook. GPS has explicit fix/no-fix and standards-shaped
  `ddmm.mmmm`/`dddmm.mmmm` output.
- Performance: the UI logic analyzer samples only while its tab is open or a
  trigger is armed, digital traces read a bit-parallel level mask, and timer
  IRQ resynchronisation and hang sampling were trimmed. A 20 s Touch-LCD-2 run
  with a WASM UART plugin went from 19 s to 9 s in the UI and from 9.8 s to
  6.8 s headless.

## v0.4 highlights

v0.4 adds observability (profiling, coverage and hang explanations), deeper
peripheral fidelity (a paced ADC and a virtual BOOTSEL drive), and a faster
instruction core.

- Peripheral scheduling no longer polls every cycle. WS2812B, audio
  (I2S/PDM/PWM) and the W5500/W6300 Ethernet models observe pin edges through
  listeners, and their timed work (the WS2812B 50 µs reset gap, the audio sample
  window, a scripted touch) runs on the emulator's virtual-time calendar. Boards
  with these devices keep the idle fast-forward when they are idle: an idle
  audio-plus-LED board went from about 0.30× to 9.7× real time on a desktop
  host. Decoder output and Ethernet behaviour are unchanged.
- USB HID joins CDC and MIDI. The emulated host follows the configuration
  descriptor for HID interfaces, fetches the report descriptor, sends SET_IDLE,
  and decodes keyboard, mouse and gamepad reports (including composite devices
  and report IDs). `-usb-hid` logs reports, `-hid-out` sends host-to-device
  output such as keyboard LEDs, and the new **HID** tab monitors reports and
  sends output reports.
- The scope gains a **Show FFT** view: a Hann-windowed one-sided real FFT of
  the visible window, dBFS-normalised, with a log/linear frequency axis, channel
  selector, interpolated peak readout and `--fft` for headless renders. The FFT
  is UI-only (`github.com/go-fft/fft` in `cmd/simui`); the root module remains
  dependency-free. Spectrum and time plots resize with the window.
- Scope measurements now include 10–90% rise and fall times per channel,
  alongside minimum/maximum levels and the existing frequency/duty/period
  readouts. A per-channel toggle switches between logic edges and analog
  10–90% thresholds, and `--measure` renders the analog view headlessly.
- A virtual RPI-RP2 BOOTSEL drive: the **BOOTSEL** tab takes a `.uf2` through a
  file picker or path, accepts any RP2350 UF2 family, installs only flash
  segments after validating the boot vector, then boots the new image. The
  browser page also accepts a dropped file; the desktop build uses the
  click/path flow because Shirei exposes no native OS drop event.
- Hang detection: the chip samples the PC every 64 cycles and reports a core
  that stays in a tight loop, with the function/line and the polled register
  resolved through DWARF and the official `RP2350.svd` (for example
  `PLL_SYS_PWR.PD is set, so LOCK cannot set`). The UI shows a HANG banner,
  `-hang-cycles` controls or disables the detector, and `OnHang`/`HangInfo`
  expose it to tools. Only peripheral polls and pure spins count, so legitimate
  SRAM busy-waits such as `hello_timer` are not false positives. The PLL model
  now gates `LOCK` on `PWR.PD`/`VCOPD`.
- The ADC is paced instead of instantaneous: conversions take 96 ADC clocks
  scaled by `DIV` (500 kS/s at `DIV=0`), with an 8-entry FIFO, START_ONCE /
  START_MANY, round-robin `AINSEL` updates, FCS threshold IRQ 35 and
  `DREQ_ADC` pacing for DMA channels. Board analog sources gain ramp and noise
  waveforms, live mute/waveform switching in the Scope tab, and the
  `pico2-adc` board exposes sine/ramp/noise/square on GP26–29.
- Instruction predecode: both cores share a bounded 16K-entry direct-mapped
  cache of fetched Thumb halfwords, keyed by PC and a memory generation. Flash
  erase/program/load, QMI/XIP writes, writable PSRAM stores and reset bump the
  generation; CPU/DMA SRAM stores clear only the overlapping entries, so
  self-modifying code stays correct. `blink.elf` runs about 8% faster.
- Profiling and coverage: `-profile` samples the PC every `-profile-interval`
  cycles and counts every executed instruction exactly. `-coverage PATH` writes
  LCOV, `-profile-out PATH` writes the report as JSON, and `-profile-top N`
  logs the hottest functions. The new **Profile** tab shows the summary, a
  function table sorted by sampled cycles with instruction coverage, the IRQ
  pend-to-entry latency histogram, and sampled MSP/PSP low-water marks. All of
  it runs on virtual time, so repeated runs produce the same report.
- Selecting a board before loading a firmware keeps the machine parked. It used
  to execute the empty boot ROM and slide through unmapped memory, which showed
  up as about 0.03× in the UI and grew the unmapped-access map.
- Verification covers unit, race, lint, QEMU differential, WASM build and the
  full firmware smoke suite. New regressions cover ADC pacing/FIFO/DMA,
  predecode invalidation, hang false positives, profile LCOV/JSON and the
  Profile tab snapshot, and the pico-examples timer checks exercise the
  busy-wait path.

## v0.3 highlights

v0.3 focuses on virtual-time scheduling, desktop and browser performance, and
UI fidelity.

- Emulator interval samplers (scope and logic traces, analog waveform sources,
  IMU, SD write-back, CH9120 and touch) moved to `Chip.AddPeriodicHook`: they
  run on their virtual-time grid instead of every cycle, so they keep the idle
  fast-forward available. CLI `-logic` captures and the UI scope/logic tabs no
  longer cost throughput, and a pin trace takes its first sample at attach so
  the capture window starts where it used to. Missed grid slots are skipped,
  never replayed.
- Desktop UI pacing: a live window keeps virtual time level with wall time
  instead of running flat out. The status line shows the achieved ratio over a
  one-second window, turning the virtual clock red below 0.90 and clearing it
  above 0.97; the new `--turbo` flag runs as fast as the host allows. Headless
  snapshots are unaffected.
- UI loop: the 200 µs pacing sleep (about a millisecond of timer granularity on
  Linux) is gone and repaint requests are capped at display rate. Measured on
  the Touch-LCD-2 the window now runs at 1.0× real time (1.5× with `--turbo`);
  the GEEK LCD example went from 0.11× to about 0.25×.
- ST7789 frame memory is addressed column-wise under MADCTL MV (`X*240+Y`), as
  the controller does. The Touch-LCD-2 no longer duplicates the right third of
  the panel on the left, which made a touch marker near an edge look like a copy
  on the opposite side; the GEEK output is unchanged.
- Homebrew formula generation targets macOS only (Apple Silicon and Intel).
- Browser build: `task wasm:build` compiles the desktop UI to WebAssembly with
  Shirei's `shirei_web`, so the same window runs in a page with the board,
  firmware and tab chosen by URL (`?board=…&fw=…&tab=…`). The demo is published
  with the documentation at <https://akhenakh.github.io/rp2350sim/demo/>. Host
  devices (sockets, SD directory backing, GDB, the file picker) are stripped
  before boot and firmware is fetched over HTTP. The browser starts in turbo
  and hides host file/audio controls; see `docs/wasm.md`.
- Emulator hot-path pass: a one-entry MMIO register-block cache, monitor
  invalidation without a per-write slice, cached PIO input levels, hoisted
  SysTick/SHA/HSTX enable checks, a flat-XIP instruction-fetch path, and bus
  taps that only format a trigger message when a trigger is armed. About 17%
  more instructions per second natively (~36M/s) and ~24% on the LCD demo; the
  browser build gains ~23% (2.6M → 3.2M cycles/s) plus `wasm-opt -O3`.

## v0.2 highlights

v0.2 contains 71 commits since `093f67a`, focused on RP2350
datasheet conformance and regression hardening.

- RP2350 datasheet-conformance pass across CPU/SCS, memory/boot, DMA, PIO,
  timers, PWM, ADC, serial peripherals, USB, HSTX, TRNG, SHA-256 and OTP.
- Host-safety fixes for FAT16 confinement, flash backing, GDB locking, bounded
  network I/O, socket shutdown and device lifecycle cleanup.
- TinyGo reliability: USB IRQ delivery, UART host bursts, PSP exception-entry
  synchronization and multicore console completion.
- DMA/PIO/PWM/timer/ADC trigger, FIFO, interrupt, timing and reset semantics
  corrected, with new conformance and idle-scheduling regressions.
- I2C/SPI/UART abort, FIFO, threshold, reset and narrow-write behavior aligned
  with the RP2350 datasheet.
- PSRAM maintenance-window safety, BOOTRAM lane writes, `+0x4000` zero-fill
  writes, PSM core-1 reset and PADS IE/ISO/OD behavior added.
- Desktop UI migrated to Shirei v0.10 with the in-window `FileBrowserModal`;
  Zenity and its transitive dependencies were removed.
- GEEK ST7789 logical-window rendering fixed, and the oscilloscope GPIO picker
  is now width-aware and stays inside the window.
- Debugger source rendering guards stale DWARF line tables; board validation
  rejects GPIO/chip-select collisions.
- Verification expanded across unit/race/lint/QEMU plus full TinyGo, Pico SDK,
  pico-examples, UI, network, bridge, LCD, DVI, OTA, PSRAM, OTP and RTOS smokes.

## Verifying a release

The release workflow attests every archive and the checksum file with GitHub
build provenance. After downloading an archive, verify it with the GitHub CLI:

```sh
gh attestation verify rp2350sim_<version>_linux_amd64.tar.gz \
  --repo akhenakh/rp2350sim
```

The `.zip` Windows archives work the same way.

## Homebrew tap

Every `v*` release also updates
[`akhenakh/homebrew-tap`](https://github.com/akhenakh/homebrew-tap). Install
both programs on macOS (Apple Silicon or Intel) with:

```sh
brew install akhenakh/tap/rp2350sim
```

The release workflow runs `scripts/brew-bump.sh <tag>` after the GitHub
release and artifact attestations. The script downloads the release checksums,
generates `Formula/rp2350sim.rb` with the two macOS platform URLs and
SHA-256 digests, and pushes it to the tap. It uses the repository secret
`TAP_GITHUB_TOKEN`, a token with `contents: write` on the tap. Without the
token it falls back to an SSH clone.

To check the generated formula locally without touching the tap:

```sh
DRY_RUN=1 CHECKSUMS_FILE=dist/rp2350sim_<version>_checksums.txt \
  scripts/brew-bump.sh v<version>
```

The formula installs `rp2350sim` and `rp2350sim-ui` and tests both version
commands.

## AUR

The Arch package is `rp2350sim` in the AUR and builds both programs from the
release source tarball. Install it with an AUR helper:

```sh
yay -S rp2350sim
# or
paru -S rp2350sim
```

The package installs `/usr/bin/rp2350sim` and `/usr/bin/rp2350sim-ui`. The
PKGBUILD tracks the latest published tag and carries that tag's source
checksum. After a release, bump `pkgver` to the new tag, run `updpkgsums`, and
regenerate `.SRCINFO` with `makepkg --printsrcinfo > .SRCINFO` before updating
the AUR. Use the abstract source path
`https://github.com/akhenakh/rp2350sim/archive/refs/tags/<tag>.tar.gz` so the
PKGBUILD is not coupled to an unreleased version.

## Local checks

```sh
task release:check      # goreleaser check
task release:snapshot   # build all archives under dist/, no publish
```

`task release:snapshot` needs GoReleaser installed (`goreleaser` in `$PATH`).
The release workflow pins the v2 GoReleaser action and attests the published
archives and checksums with GitHub artifact attestations.

## Licensing

The project is Apache-2.0. The `LICENSE` file is included in every archive;
the emulator's bundled board definitions and documentation are covered by the
same license.
