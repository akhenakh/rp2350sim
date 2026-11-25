#!/usr/bin/env bash
# Generate the Homebrew formula for a release and push it to
# akhenakh/homebrew-tap.
#
# Usage: scripts/brew-bump.sh <tag>    (e.g. scripts/brew-bump.sh v0.2)
#
# Requires write access to akhenakh/homebrew-tap, either via a
# TAP_GITHUB_TOKEN (a PAT with contents:write on the tap) or an SSH key.
#
# Environment:
#   DRY_RUN=1          print the formula and exit without touching the tap
#   CHECKSUMS_FILE=... use a local checksums file instead of downloading it
set -euo pipefail

TAG="${1:?usage: $0 <tag>}"
VERSION="${TAG#v}"

BIN="rp2350sim"
UI="rp2350sim-ui"
CLASS="Rp2350sim"
REPO="akhenakh/rp2350sim"
DESC="RP2350 emulator with a desktop UI"
LICENSE="Apache-2.0"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [[ -n "${CHECKSUMS_FILE:-}" ]]; then
	cp "$CHECKSUMS_FILE" "$TMP/checksums.txt"
else
	curl -fsSL "https://github.com/${REPO}/releases/download/${TAG}/${BIN}_${VERSION}_checksums.txt" \
		-o "$TMP/checksums.txt"
fi

sha() {
	awk -v f="$1" '$2 == f { print $1 }' "$TMP/checksums.txt"
}

DARWIN_AMD64="$(sha "${BIN}_${VERSION}_darwin_amd64.tar.gz")"
DARWIN_ARM64="$(sha "${BIN}_${VERSION}_darwin_arm64.tar.gz")"

for v in DARWIN_AMD64 DARWIN_ARM64; do
	if [[ -z "${!v}" ]]; then
		echo "error: missing checksum for ${v}" >&2
		exit 1
	fi
done

cat >"$TMP/${BIN}.rb" <<EOF
# typed: false
# frozen_string_literal: true

class ${CLASS} < Formula
  desc "${DESC}"
  homepage "https://github.com/${REPO}"
  # Apple Silicon is the default; on_intel overrides it.
  url "https://github.com/${REPO}/releases/download/${TAG}/${BIN}_${VERSION}_darwin_arm64.tar.gz"
  sha256 "${DARWIN_ARM64}"
  license "${LICENSE}"

  # Homebrew on Linux is not supported; Linux users install the release
  # tarball directly.
  depends_on :macos

  on_macos do
    on_intel do
      url "https://github.com/${REPO}/releases/download/${TAG}/${BIN}_${VERSION}_darwin_amd64.tar.gz"
      sha256 "${DARWIN_AMD64}"
    end
  end

  def install
    bin.install "${BIN}"
    bin.install "${UI}"
  end

  test do
    assert_match "${BIN}", shell_output("#{bin}/${BIN} -version")
    assert_match "${UI}", shell_output("#{bin}/${UI} --version")
  end
end
EOF

if [[ "${DRY_RUN:-}" == "1" ]]; then
	cat "$TMP/${BIN}.rb"
	exit 0
fi

TAP_DIR="$TMP/tap"
if [[ -n "${TAP_GITHUB_TOKEN:-}" ]]; then
	git clone "https://x-access-token:${TAP_GITHUB_TOKEN}@github.com/akhenakh/homebrew-tap.git" "$TAP_DIR"
else
	git clone "git@github.com:akhenakh/homebrew-tap.git" "$TAP_DIR"
fi
mkdir -p "$TAP_DIR/Formula"
cp "$TMP/${BIN}.rb" "$TAP_DIR/Formula/${BIN}.rb"

git -C "$TAP_DIR" config user.email "akh@inair.space"
git -C "$TAP_DIR" config user.name "Fabrice Aneche"
git -C "$TAP_DIR" add "Formula/${BIN}.rb"
git -C "$TAP_DIR" commit -m "Brew formula update for ${BIN} ${TAG}" || true
git -C "$TAP_DIR" push origin main
