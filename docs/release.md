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

Replace `<tag>` with the release tag (for example `v0.2.0`) and `<previous-tag>`
with the prior tag. Do not hardcode the next version in committed documentation:

- repository: `https://github.com/akhenakh/rp2350sim`
- releases: `https://github.com/akhenakh/rp2350sim/releases`
- tag: `https://github.com/akhenakh/rp2350sim/releases/tag/<tag>`
- asset: `https://github.com/akhenakh/rp2350sim/releases/download/<tag>/<asset>`
- source archive: `https://github.com/akhenakh/rp2350sim/archive/refs/tags/<tag>.tar.gz`
- comparison: `https://github.com/akhenakh/rp2350sim/compare/<previous-tag>...<tag>`

## Release sequence

1. Update this document and the user-facing docs, then commit them. Keep the
   **Next release** section current as work lands, and rename it to the version
   at tag time.
2. Tag that documentation commit as `v<version>` and push the tag.
3. The release workflow builds, attests and publishes the archives.
4. Update the Homebrew tap and AUR package from the published checksums.

Committing the release notes before tagging keeps the tag, source archive and
release page consistent without retroactively editing the tagged commit.

## Next release (unreleased)

Working notes for the next release, filled in as changes land. At tag time rename
this heading to the version and trim anything that no longer applies.

- Emulator interval samplers (scope and logic traces, analog waveform sources,
  IMU, SD write-back, CH9120 and touch) moved to `Chip.AddPeriodicHook`: they
  run on their virtual-time grid instead of every cycle, so they keep the idle
  fast-forward available. CLI `-logic` captures and the UI scope/logic tabs no
  longer cost throughput, and a pin trace takes its first sample at attach so
  the capture window starts where it used to.
- Desktop UI pacing: a live window keeps virtual time level with wall time
  instead of running flat out. The status line shows the achieved ratio and
  turns the virtual clock red when the interpreter cannot keep up; the new
  `--turbo` flag runs as fast as the host allows. Headless snapshots are
  unaffected.
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
  before boot and firmware is fetched over HTTP; see `docs/wasm.md`.
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
