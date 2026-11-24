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

The Arch package is published as `rp2350sim` in the AUR and builds both
programs from the release source tarball. Install it with an AUR helper:

```sh
yay -S rp2350sim
# or
paru -S rp2350sim
```

The package installs `/usr/bin/rp2350sim` and `/usr/bin/rp2350sim-ui`. The
current PKGBUILD targets `v0.1.0`; after that tag is published, update its
checksum with `updpkgsums` and regenerate `.SRCINFO` with
`makepkg --printsrcinfo > .SRCINFO` before uploading.

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
