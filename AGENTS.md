# Agent notes

## What this project is

Go emulator for the RP2350 (Pico 2) targeting TinyGo firmware, with a Shirei UI
planned on top. Hard targets: Waveshare RP2350-POE-ETH (W6300 Ethernet) and
RP2350-GEEK (ST7789VW 135x240 LCD).

## Facts established by investigation (do not re-derive)

- TinyGo `-target=pico2` emits **Arm Cortex-M33** (`thumbv8m.main`, soft-float,
  +dsp, no FPU/MVE/PACBTI). There is no TinyGo RISC-V rp2350 target.
- Stock TinyGo firmware always launches core 1 through the SIO FIFO bootrom
  handshake (`0,0,1,vector,sp,entry`, each word echoed) and uses SIO spinlocks.
- TinyGo images need no bootrom: vector table at 0x10000000, `.data` LMA in
  flash, reset handler copies/zeros. `SP=[0x10000000]`, `PC=[0x10000004]`.
- TinyGo's SPI driver always uses DMA, including for small transfers.
- TinyGo `net` is a BSD-socket offload shim (`netdev`); the W5500 driver uses the
  chip's hardware sockets. W6300 (POE-ETH board) has a different register map,
  SPI framing with a dummy byte, and 32+32 KB buffers; no TinyGo driver exists.
- Waveshare RP2350-POE-ETH wiring: SPI0, SCK=GP18, MOSI=GP19, MISO=GP16,
  CS=GP17, RST=GP20, INT=GP21, 70 MHz, QSPI-capable but wired single-SPI.
- Waveshare RP2350-GEEK: ST7789VW, 135x240 (240x135 landscape with MADCTL=0x70,
  offsets x+40 y+53), SPI1 SCK=GP10 MOSI=GP11, CS=GP9, DC=GP8, RST=GP12,
  BL=GP25, microSD on GP18-23, I2C on GP28/29.

## Debugging tools in `cmd/rp2350sim`

`-trace`, `-trace-last`, `-bp`, `-bp2`, `-watch`, `-poke`, `-strict-mmio`.

TracePC only fires for CPU0 and is installed only with `-trace-last`.

## CPU implementation notes

- Banked SP: `MSP`/`PSP` plus `CONTROL.SPSEL`; TinyGo's scheduler context switch
  writes PSP and toggles CONTROL.
- IT state is an 8-bit ITSTATE; do not advance it on the instruction that sets
  it.
- 16-bit conditional branches are 0x1A *and* 0x1B (cond in bits 11..8); 0x1C is
  the unconditional branch.
- CBZ/CBNZ need mask 0xFD00 (0xF500 confuses them).
- 32-bit load/store: bit7 of hw1 selects imm12 (T3); T2 register offset only when
  that bit is clear; T4 when hw2 bit11 is set.
- MUL (T2) is signalled by hw2[15:12] == 0b1111; MLA/MLS otherwise.
- STRD/LDRD immediate: hw1[7:5] = U/P/W, hw2[7:0] = imm8*4; the family requires
  hw1 bits 7..6 not both zero (otherwise it is STMDB).
- B.W (T4) and BL use S:J1:J2:imm10:imm11 with I = NOT(J XOR S); B<c>.W (T3)
  uses S:J2:J1:imm6:imm11 and sign-extends from bit 20.

## Fixed bugs worth remembering

- 16-bit `add Rd, SP, #imm` is opcode 0x15; implementing it as `add SP` silently
  corrupted SP throughout the firmware.
- 16-bit LDM must not write back Rn when Rn is in the register list.
- 16-bit conditional branch is 0x1A/0x1B, not 0x1A.
- CBZ/CBNZ discriminators are 0xFD00.
- 32-bit LDR/STR: imm12 when hw1 bit7 is set; register offset otherwise (unless
  hw2 bit11 marks the T4 form).
- MUL is selected by hw2[15:12] == 0b1111.
- Register shifts (LSL/LSR/ASR/ROR) live in the 0xFA group with hw2[7:4] == 0.
- IT state must not advance on the instruction that sets it.

## Current state

With the instruction fixes above, the firmware completes TinyGo's package
initialisation (PLL divisors come out as 125/5/2) and reaches `runtime.initAll`
completion, but hangs in the heap allocator/GC path before `main`. With
`-gc=leaking` it diverges elsewhere, so there is at least one remaining
instruction bug exercised by runtime code.

## Next step

Build the QEMU differential harness (`qemu-system-arm -M mps2-an505`, GDB stub)
to compare register state instruction-by-instruction, then use it to find the
remaining divergence in TinyGo's PLL search loop.
