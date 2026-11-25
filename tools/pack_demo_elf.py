#!/usr/bin/env python3
"""Pack a Pico 2 firmware ELF for the browser demo.

The simulator loader reads only PT_LOAD program headers; section headers and
DWARF are ignored. The Pico 2 linker aligns segments to 64 KiB, so a debug ELF
carries large holes that the browser downloads for nothing. This rewrites the
loadable flash image as a single PT_LOAD segment, keeping the bytes the loader
uses and dropping the rest.

Usage: tools/pack_demo_elf.py firmware.elf site/static/demo/firmware/name.elf
"""
from __future__ import annotations

import struct
import sys
from pathlib import Path

ELF_HEADER = struct.Struct("<16sHHIIIIIHHHHHH")
PROGRAM_HEADER = struct.Struct("<IIIIIIII")
PT_LOAD = 1
PF_R = 4
PF_X = 1
FLASH_BASE = 0x10000000
FLASH_SIZE = 16 * 1024 * 1024


def fail(message: str) -> None:
    raise SystemExit(f"pack_demo_elf: {message}")


def main() -> None:
    if len(sys.argv) != 3:
        fail("usage: pack_demo_elf.py IN.elf OUT.elf")
    src, dst = map(Path, sys.argv[1:])
    data = src.read_bytes()
    if len(data) < ELF_HEADER.size:
        fail(f"{src} is too small to be an ELF")

    fields = ELF_HEADER.unpack_from(data)
    ident, e_type, e_machine = fields[0], fields[1], fields[2]
    e_entry, e_phoff, e_flags = fields[4], fields[5], fields[7]
    e_phentsize, e_phnum = fields[9], fields[10]
    if ident[:4] != b"\x7fELF" or ident[4] != 1 or ident[5] != 1:
        fail("expected a 32-bit little-endian ELF")
    if e_type != 2 or e_machine != 40:
        fail("expected an ARM executable")
    if e_phentsize != PROGRAM_HEADER.size:
        fail(f"unexpected program header size {e_phentsize}")

    segments: list[tuple[int, bytes]] = []
    for i in range(e_phnum):
        off = e_phoff + i * e_phentsize
        if off + e_phentsize > len(data):
            fail("program header table is truncated")
        p_type, p_offset, _, p_paddr, p_filesz = PROGRAM_HEADER.unpack_from(data, off)[:5]
        if p_type != PT_LOAD or p_filesz == 0:
            continue
        if p_offset + p_filesz > len(data):
            fail(f"segment {i} extends past the file")
        segments.append((p_paddr, data[p_offset:p_offset + p_filesz]))
    if not segments:
        fail("no loadable segments")

    base = min(addr for addr, _ in segments)
    end = max(addr + len(blob) for addr, blob in segments)
    if base < FLASH_BASE or end > FLASH_BASE + FLASH_SIZE:
        fail(f"segments {base:#x}..{end:#x} are outside the flash window")

    image = bytearray(end - base)
    for addr, blob in segments:
        image[addr - base : addr - base + len(blob)] = blob

    payload = 0x100
    header = bytearray(ELF_HEADER.size)
    ELF_HEADER.pack_into(
        header,
        0,
        b"\x7fELF\x01\x01\x01" + b"\x00" * 9,
        2,
        40,
        1,
        e_entry,
        ELF_HEADER.size,
        0,
        e_flags,
        ELF_HEADER.size,
        PROGRAM_HEADER.size,
        1,
        0,
        0,
        0,
    )
    program = PROGRAM_HEADER.pack(
        PT_LOAD, payload, base, base, len(image), len(image), PF_R | PF_X, 4
    )
    out = header + program
    out += b"\x00" * (payload - len(out))
    out += image

    dst.parent.mkdir(parents=True, exist_ok=True)
    dst.write_bytes(out)
    print(
        f"pack_demo_elf: {src} ({len(data)} B) -> {dst} "
        f"({len(out)} B, flash {base:#x}..{end:#x})"
    )


if __name__ == "__main__":
    main()
