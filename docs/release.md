# Releases

Releases are built with [GoReleaser](https://goreleaser.com) from the tag
workflow. Pushing a `v*` tag (or dispatching the `release` workflow with a tag)
publishes a GitHub release with both programs:

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
