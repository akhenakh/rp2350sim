#!/usr/bin/env python3
"""Generate the Hugo content tree from the repository's docs/ directory.

The Markdown files stay the source of truth for GitHub readers and release
archives. Hugo needs front matter and page-relative links, so this script
copies them into site/content/docs/ and copies images into site/static/docs/.
The generated directories are ignored by git.
"""
from __future__ import annotations

import re
import shutil
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DOCS = ROOT / "docs"
SITE = ROOT / "site"
CONTENT = SITE / "content" / "docs"
STATIC_IMAGES = SITE / "static" / "docs" / "images"

# Order and display titles for the sidebar.
PAGES = [
    ("README.md", "Documentation", 1),
    ("install.md", "Installation", 2),
    ("videos.md", "Videos", 5),
    ("cli.md", "Command-line reference", 10),
    ("peripherals.md", "Peripherals from CLI and UI", 20),
    ("bus-pirate.md", "Bus Pirate targets", 25),
    ("ui.md", "Desktop UI", 30),
    ("boards-yaml.md", "boards.yaml reference", 32),
    ("wasm.md", "Browser build", 35),
    ("debug.md", "Debugging", 40),
    ("profiler.md", "Profiler and coverage", 41),
    ("hid.md", "USB HID", 42),
    ("plugins.md", "Device plugins", 43),
    ("boards.md", "Boards and peripherals", 50),
    ("tinygo.md", "TinyGo", 60),
    ("c.md", "Pico SDK C", 70),
    ("zephyr.md", "Zephyr", 75),
    ("ci.md", "CI", 80),
    ("release.md", "Releases", 90),
]

LINK = re.compile(r"(\!?\[[^\]]*\]\()([^)]+)(\))")


def rewrite_links(text: str, index: bool) -> str:
    """Make docs links work from /docs/<page>/ in Hugo."""
    prefix = "" if index else "../"

    def repl(m: re.Match[str]) -> str:
        target = m.group(2)
        if target.startswith(("http://", "https://", "mailto:", "#", "/")):
            return m.group(0)
        if target.startswith("images/"):
            return m.group(1) + prefix + target + m.group(3)
        path, _, anchor = target.partition("#")
        if path.endswith(".md"):
            stem = path[:-3]
            if stem in ("README", "readme"):
                stem = ""
            link = prefix + stem
            if stem:
                link += "/"
            if anchor:
                link += "#" + anchor
            return m.group(1) + link + m.group(3)
        return m.group(0)

    return LINK.sub(repl, text)


def main() -> None:
    if not DOCS.is_dir():
        raise SystemExit(f"{DOCS} does not exist")
    if CONTENT.exists():
        shutil.rmtree(CONTENT)
    CONTENT.mkdir(parents=True)
    if STATIC_IMAGES.exists():
        shutil.rmtree(STATIC_IMAGES)
    STATIC_IMAGES.mkdir(parents=True)

    for name, title, weight in PAGES:
        src = DOCS / name
        if not src.exists():
            continue
        text = src.read_text()
        # Drop the original H1: Geekdoc renders the title from front matter.
        text = re.sub(r"\A# .*\n+", "", text)
        index = name == "README.md"
        text = rewrite_links(text, index)
        if index:
            out = CONTENT / "_index.md"
        else:
            out = CONTENT / name.removesuffix(".md") / "index.md"
            out.parent.mkdir(parents=True, exist_ok=True)
        out.write_text(
            f"---\ntitle: {title!r}\nweight: {weight}\ntoc: true\n---\n\n{text.lstrip()}"
        )

    for image in sorted((DOCS / "images").glob("*")):
        if image.is_file():
            shutil.copy2(image, STATIC_IMAGES / image.name)

    print(f"generated {len(PAGES)} docs pages in {CONTENT}")
    print(f"copied images to {STATIC_IMAGES}")


if __name__ == "__main__":
    main()
