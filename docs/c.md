# C firmware (Pico SDK)

`rp2350sim` runs stock [Pico SDK](https://github.com/raspberrypi/pico-sdk) C
firmware for the RP2350. No patches and no special build flags: build with
CMake for `PICO_BOARD=pico2` and give the `.elf` (or `.uf2`) to the emulator.

TinyGo remains the primary contract; the C path is additive and the smoke suite
runs both.

## Prerequisites

- `arm-none-eabi-gcc` and newlib (for example `gcc-arm-none-eabi` from your
  distribution, or the ARM GNU toolchain tarball).
- Pico SDK 2.x. Point `PICO_SDK_PATH` at the checkout.
- CMake and Ninja (or Make).

```sh
export PICO_SDK_PATH=/path/to/pico-sdk
```

## Build and run the examples

The repository keeps a small CMake project in `firmware/c`:

```sh
task c:build      # cmake + ninja, PICO_BOARD=pico2
task c:hello      # build and assert "hello c done"
task c:blinky     # build and trace GP25
task c:float      # single-precision FPU demo
task c:multicore  # core 1 launch through the SIO FIFO
task c:psram      # QSPI PSRAM size and pattern check
task c:debug      # stop at debug_marker with known variables
```

The same by hand:

```sh
cd firmware/c
cmake -S . -B build -G Ninja -DPICO_BOARD=pico2 \
  -DPICO_SDK_PATH="$PICO_SDK_PATH" -DCMAKE_BUILD_TYPE=Release
cmake --build build
rp2350sim -fw build/hello.elf -seconds 1
```

The runner takes `.elf`, `.uf2` and raw images. UF2 family
`0xe48bff59` (RP2350 ARM-S) is validated.

A new project needs only the usual SDK CMake boilerplate; nothing in the
emulator is project-specific.

## Console

The examples enable **UART stdio** (`pico_enable_stdio_uart(app 1)`). Firmware
`printf` output goes to UART0 and therefore to the runner's stdout, exactly like
TinyGo's console:

```sh
rp2350sim -fw build/hello.elf -seconds 1 -expect "hello c done"
```

USB CDC stdio (`pico_enable_stdio_usb`) is not yet part of the verified path;
keep UART enabled while the TinyUSB device stack is brought up.

`pico_time` works: `sleep_ms`/`sleep_us`, `time_us_32/64`, alarms and the
default alarm pool are driven by the emulated 1 MHz timer. `pico_multicore`
works: `multicore_launch_core1` uses the same SIO FIFO handshake as TinyGo.

## What runs today

- Boot: the SDK flash image layout (`.vectors` first, bootrom skipped) boots
  through the reset handler and `runtime_init`.
- Bootrom services: `rom_func_lookup`/`rom_table_lookup`, bootrom state reset,
  boot stack, boot random, memory helpers, flash range erase/program, and
  explicit "unsupported" replies for partition/secure services. See
  `internal/rp2350/bootrom.go` for the synthetic table.
- Clocks/resets/timer/GPIO coprocessor/SIO/NVIC: enough for the SDK runtime,
  `printf`, GPIO and multicore.
- Single-precision floating point (VFPv5-SP): arithmetic, comparisons,
  conversions, loads/stores and callee-saved `d8`-`d15` push/pop. The
  `floatdemo` example asserts exact IEEE bit patterns.
- Double-precision arguments to `printf` (`%f`) are **not** correct yet: the
  SDK routes libgcc's `__aeabi_d*` through the RP2350 double coprocessor
  (DCP, CP4), and the emulator's functional DCP model handles arithmetic and
  conversions but not the vararg/word-order path used by `printf`. Print
  integer bit patterns instead of `%f` until that lands.

## Debugging

`firmware/c/debug` is a deterministic fixture for the Debug tab and the CLI:
`debug_marker` has `g_magic`, two 32-bit `g_wide_*` globals and a volatile
`local`, all checked by `internal/debug`. Use `task c:debug`, `task debug:test`
or `task ui:debug`; see [debug.md](debug.md).

The runner flags are the same as for TinyGo (`-trace`, `-bp`, `-trace-pins`,
`-lcd`, `-serial-tcp`, `-expect`, ...). `-trace-last` prints a ring of recent
instructions when the core halts, and `RP2350SIM_DCP_LOG=1` logs every double
coprocessor operation with its operands and result.

## Limitations

- The DCP double path is incomplete (see above); single precision is complete.
- Bootrom coverage is a functional subset. Firmware that calls `rom_reboot`,
  `reset_usb_boot`, partition/OTP services or the ROM SF table halts with a
  clear "unsupported service" message rather than executing ROM zeros.
- USB device stdio is not validated yet; use UART stdio.
- The RP2350 bootrom is not emulated; the image is entered at its vector table.
  `PICO_NO_FLASH` / `PICO_COPY_TO_RAM` images are out of scope.
