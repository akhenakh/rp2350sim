# Installation

`rp2350sim` ships two programs:

- `rp2350sim` — the headless runner and debugging tools.
- `rp2350sim-ui` — the Shirei desktop UI.

## macOS (Apple Silicon and Intel)

Install both programs with [Homebrew](https://brew.sh):

```sh
brew install akhenakh/tap/rp2350sim
```

## Arch Linux

Install both programs from the AUR with an AUR helper:

```sh
yay -S rp2350sim
# or
paru -S rp2350sim
```

The AUR package builds both programs from the release source tarball.

## Prebuilt archives

Download an archive from [GitHub Releases](https://github.com/akhenakh/rp2350sim/releases).
Each archive contains `rp2350sim`, `rp2350sim-ui`, `LICENSE`, `README.md` and
the `docs/` directory.

| platform | archive |
|---|---|
| Linux (`amd64`, `arm64`) | `rp2350sim_<version>_linux_<arch>.tar.gz` |
| macOS (`amd64`, `arm64`) | `rp2350sim_<version>_darwin_<arch>.tar.gz` |
| Windows (`amd64`, `arm64`) | `rp2350sim_<version>_windows_<arch>.zip` |
| checksums | `rp2350sim_<version>_checksums.txt` |

Extract the archive and put both binaries on your `PATH`. The binaries are
pure Go; no C toolchain is required at runtime. The UI uses the system
graphics and keyboard libraries and, on Linux, may use Mesa and ALSA.

## Build from source

Requires Go 1.27 or newer and Git. The desktop UI is a separate Go module, so
build it explicitly:

```sh
git clone https://github.com/akhenakh/rp2350sim.git
cd rp2350sim
go build -o build/rp2350sim ./cmd/rp2350sim
(cd cmd/simui && go build -o ../../build/rp2350sim-ui .)
```

The headless runner can also be installed into `GOBIN`:

```sh
go install ./cmd/rp2350sim
```

## Verify a download

Release archives and the checksum file carry GitHub build provenance. After
downloading an archive, verify it with the GitHub CLI:

```sh
gh attestation verify rp2350sim_<version>_linux_amd64.tar.gz \
  --repo akhenakh/rp2350sim
```

The `.zip` Windows archives work the same way. See the [release
guide](release.md) for the artifact layout and signing details.

## Firmware toolchains

This page installs the emulator only. To build firmware for it, see
[TinyGo](tinygo.md), [Pico SDK C](c.md) and [Zephyr](zephyr.md).
