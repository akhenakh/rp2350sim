# Debugging firmware

The desktop UI has a **Debug** tab and the headless runner accepts breakpoints by
address or symbol. Both use the same ELF debug information: function symbols,
DWARF source lines and the variables the compiler left locations for.

![Debug tab stopped at debug_marker](images/debug.png)

```sh
# UI: open the tab, stop at a symbol, and show the machine there
rp2350sim-ui --board pico2 --tab debug --break debug_marker firmware.elf

# UI: open the inspector on Variables (or memory) instead of registers
rp2350sim-ui --board pico2 --tab debug --break main.marker \
  --debug-tab variables build/debugvars.elf

# Headless: log a breakpoint and continue
rp2350sim -fw firmware.elf -seconds 2 -bp debug_marker

# Watch a symbol or address
rp2350sim -fw firmware.elf -seconds 2 -watch g_magic
```

## The Debug tab

- **Execution**: Run, Pause, Step one instruction, Run N cycles and Run to an
  absolute cycle count. A Step executes one instruction on the selected core;
  the other core stays halted. Cycle counts are virtual 150 MHz cycles, so
  exceptions and slow external memory are visible in the delta.
- **Breakpoints** can be entered as `0x...` or as an ELF symbol
  (`main.main`, `debug_marker`, `main`). The list shows the resolved address,
  function and source line. Address breakpoints also work for UF2/raw images,
  which have no symbols.
- **Write watchpoints** stop when firmware writes the resolved address. A
  watchpoint is checked on 8/16/32-bit writes; the write still completes.
- **Source** shows the lines around the current PC when the DWARF file is
  present on this machine. A short name, function and `file:line` are shown
  even when the source tree is from another build host.
- The lower third of the tab is a **Registers / Memory / Variables** inspector.
  - **Registers** shows R0-R15, APSR, IPSR, CONTROL, MSP and PSP.
  - **Memory** is a read-only hex/ASCII view. It uses side-effect-free `Peek`
    reads for ROM, flash, SRAM and PSRAM: it does not charge PSRAM latency and
    never reads a UART/SPI/I2C/FIFO register. **PC** and **SP** jump to the
    current locations.
  - **Variables** evaluates the current function's locals and globals.

## Variables and limitations

The debugger uses the DWARF expressions that embedded compilers normally emit:

- constants (`DW_AT_const_value`);
- absolute addresses (`DW_OP_addr`);
- registers and register-relative values (`DW_OP_reg*`, `DW_OP_breg*`);
- stack/frame-relative slots (`DW_OP_fbreg`), using the CFA recovered from
  `.debug_frame`;
- basic arithmetic used by optimized expressions.

A value that the compiler optimized away, or a location form the evaluator does
not support, is shown as `optimized out`, `unsupported location` or
`unreadable`. It is never guessed.

Pico SDK C builds at `-O3` keep `volatile` locals, so the bundled
`firmware/c/debug` fixture exposes `local` and the globals reliably. TinyGo
programs often encode known locals as DWARF constants; the
`firmware/debugvars` fixture checks those. Full C++/Go expression evaluation,
call-stack unwinding and disassembly are not part of this debugger.

## Breakpoints from the CLI

`-bp` and `-bp2` accept a hex address or a symbol when `-fw` is an ELF with a
symbol table. `-watch` accepts the same forms. A missing symbol is a usage
error (exit 2), not a silent miss.

```sh
rp2350sim -fw firmware/c/build/debug.elf -seconds 1 -bp debug_marker \
  -log-format text
rp2350sim -fw firmware/c/build/debug.elf -seconds 1 -watch g_magic
```

The breakpoint record includes `symbol`, `file` and `line` when available. An
exact symbol match wins over a hexadecimal interpretation, so a symbol named
`face` is not mistaken for an address. `-trace`, `-bp`, `-watch` and
`-watch-val` can be combined. The existing `-trace-last`, `-poke` and `-peek`
flags remain available for non-interactive diagnosis.

`UF2` and raw binary images carry no symbol or DWARF data. Use an address, or
load the matching ELF if one is available.

## Fixtures and tests

Two deterministic fixtures exercise the debugger end to end:

- `firmware/c/debug`: `g_magic = 0x12345678`, `g_wide_lo`, `g_wide_hi`,
  `g_negative`, and a noinline `debug_marker` whose volatile `local` is
  `0xaaaaaaaa`.
- `firmware/debugvars`: a TinyGo program whose `main.marker` has the constant
  arguments `arg = 0x12345678` and `local = 0x1d3b5977`.

```sh
task debug:test     # build both fixtures and run the DWARF tests
task c:debug        # run the C fixture and stop at debug_marker
task ui:debug       # open the Debug tab on it
task ui:debug:png   # headless screenshot to build/ui-debug.png
```

The tests load the real ELFs, run the emulator to a breakpoint, evaluate the
same `VariablesAt` path the UI uses and compare the values. They skip when a
fixture has not been built.
