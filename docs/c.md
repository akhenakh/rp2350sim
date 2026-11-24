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
task c:usbhello   # TinyUSB CDC stdio ("hello usb done")
task c:usbecho    # TinyUSB CDC input echo
task c:aontimer   # POWMAN AON timer alarm
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

USB CDC stdio (`pico_enable_stdio_usb`) works with stock TinyUSB: the emulated
host reads the configuration descriptor to find the CDC interfaces and
endpoints, follows the DPRAM buffer addresses the stack programs in
`EP_CONTROL`, and implements the control status stage plus DATA0/DATA1
toggling on host OUT packets (IN packet PIDs are not validated). `task c:usbhello` asserts the output and `task c:usbecho` feeds
console input back through the CDC OUT endpoint.

`pico_time` works: `sleep_ms`/`sleep_us`, `time_us_32/64`, alarms and the
default alarm pool are driven by the emulated 1 MHz timer. `pico_aon_timer`
uses the emulated POWMAN millisecond counter (`task c:aontimer`).

The hardware crypto blocks are functional: the stock `sha/sha256` example
passes its vector and 1 MB NIST tests with and without DMA, and `system/rand`
passes its distribution checks through the TRNG. The `hstx/dvi_out_hstx_encoder`
example renders a 640x480 frame (`-dvi-png`, or the UI DVI tab). `pico_multicore`
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
- Double-precision arithmetic goes through the RP2350 double coprocessor
  (DCP, CP4). `firmware/c/dcp` asserts the IEEE bit patterns for `+ - * /` and
  `sqrt` through the SDK's canned sequences. A double `%f` value itself renders
  correctly, but arguments **after** a variadic double can be misread (the
  SDK's word-order path for doubles is not reproduced), so keep `%f` at the end
  of a format string or print integer bit patterns.

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

- The DCP arithmetic path used by `+ - * /` and `sqrt` is covered by
  `firmware/c/dcp`; `%f` varargs remain unsupported (see above).
- Bootrom coverage is a functional subset. `rom_reboot` (including the
  FLASH_UPDATE partition preference), flash erase/program, OTP access and the
  random-number service are modelled; `reset_usb_boot` logs a warning and
  resets. Partition and secure-boot services halt with a clear "unsupported
  service" message rather than executing ROM zeros.
- USB device stdio is validated end to end for TinyUSB (`hello_world/usb`,
  `firmware/c/usbhello`, `firmware/c/usbecho`) and TinyGo (`firmware/echo`).
  The host supports the CDC data/notification endpoints and USB-MIDI; other
  USB classes and isochronous endpoints are not modelled.
- The RP2350 bootrom is not emulated; the image is entered at its vector table.
  `PICO_NO_FLASH` / `PICO_COPY_TO_RAM` images are out of scope.
