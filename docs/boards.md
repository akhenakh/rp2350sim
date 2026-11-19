# Supported boards and peripherals

The emulated CPU is the ARM Cortex-M33 (`thumbv8m.main`), matching TinyGo's
`-target=pico2` output. Board wiring below matches the headless flags and the
UI board definitions.

## Raspberry Pi Pico 2 (RP2350A)

No device flags are needed. The core brings up the RP2350 peripherals TinyGo
uses: GPIO/IO_BANK0/PADS, SIO, NVIC/SCB, timers, clocks/PLL/XOSC, watchdog,
UART0/1, SPI0/1, I2C0/1, PWM, ADC, DMA, USB device + CDC console, and PIO0/1/2.
`tinygo build -target=pico2 -scheduler=cores` boots core 1.

## Waveshare RP2350-GEEK

- ST7789VW 240×135 on SPI1: CS GP9, DC GP8, RST GP12, backlight GP25.
- microSD on SPI0: SCK GP18, MOSI GP19, MISO GP20, CS GP23. The card is backed
  by the real host directory `sdcards/geek` by default: files are packed into
  a FAT16 image at startup and firmware writes are mirrored back. `-sd-dir`
  changes the directory; `-sd-image` uses a raw image instead. Use `-sd`
  (runner) or the `geek` UI board.
- The 3-pin SWD/UART and 4-pin I2C headers are pass-through connectors to the
  target board, not on-board devices.
- Use `-lcd` (runner) or the `geek` UI board; `-lcd-png out.png` exports the
  framebuffer.
- Reference firmware: `firmware/lcd`, which animates palette bands and a
  sweeping cursor so full-frame fills, partial updates and rotation are all
  exercised.

## Waveshare RP2350-Touch-LCD-2

- ST7789T3 240×320 IPS on SPI0: SCK GP18, MOSI GP19, CS GP17, DC GP16,
  RST GP20 (shared with touch), backlight GP15.
- CST816D touch on I2C0 at `0x15`: SDA GP12, SCL GP13, INT GP29, RST GP20.
  Reports finger count and raw X/Y at `0x02..0x06`; chip ID `0xA7` = `0xB6`.
- QMI8658 6-axis IMU on I2C0 at `0x6B`: INT1 GP14. Accelerometer defaults to
  ±8 g (4096 LSB/g) with +1 g on Z; the range follows CTRL2. Data-ready INT1
  is a level that clears when the output or status register is read.
- Battery monitor: GP28/ADC2 through a ÷3 divider (3.9 V battery ≈ 1.3 V).
- microSD on SPI1: SCK GP26, MOSI GP27, MISO GP24, CS GP25. Backed by
  `sdcards/touch-lcd2` by default (a real host directory, see below);
  `-sd-dir` or `-sd-image` override it.
- The camera connector (DVP on GP0-GP11, SCCB on I2C1 GP22/GP23) is a
  connector for an optional OV2640/OV5640 module and is not emulated.
- Use `-touch-lcd2` (runner) or the `touch-lcd2` UI board. `-lcd-png` exports
  the 320×240 rotated frame; `-touch x,y` injects a tap (native panel
  coordinates), and `-touch-at`/`-touch-hold` place it in virtual time.
- Reference firmware: `firmware/touchlcd`, which initialises the panel, touch,
  IMU, backlight and ADC, prints deterministic values and draws touch points.

## SD card backing

A card can be backed by a host directory (`sd.dir` in `boards.yaml`, or
`-sd-dir PATH`). The directory is packed into a 16 MiB FAT16 image at startup
and the image is mirrored back after firmware writes settle (about 50 ms of
virtual time). Drop files into the directory before starting, or read files
the firmware creates while it runs; files deleted on the card are removed
from the directory too. Host files that were not part of the card are never
deleted, and symlinks are skipped. Host edits made while the emulator is
running are picked up on the next start, not live.

The UI uses `sdcards/<board>` relative to its working directory by default.
`-sd-image PATH` uses a raw FAT image instead; `image` and `dir` are mutually
exclusive.

## Waveshare RP2350-POE-ETH (W6300)

- W6300 on SPI0: SCK GP18, MOSI GP19, MISO GP16, CS GP17.
- Use `-w6300` for the fitted controller.
- The in-tree TinyGo netdev driver is `drivers/w6300`; the chip model is
  `internal/devices/w6300.go`.
- Reference firmware: `firmware/w6300` (TCP client/server, UDP).

## W5500 (breadboard)

- W5500 on SPI0: SCK GP18, MOSI GP19, MISO GP16, CS GP17.
- Use `-eth` (runner) or the `pico2-w5500` UI board.
- The stock `tinygo.org/x/drivers/w5500` netdev driver runs unmodified; the
  chip model is `internal/devices/w5500.go`.
- Reference firmware: `firmware/w5500` (TCP client/server, UDP). The stock
  driver rejects the 16-byte IPv4-mapped address that `net.IPv4` returns, so
  the client passes a 4-byte `net.IP{127,0,0,1}`.

## Waveshare RP2350-ETH (CH9120, non-PoE)

- CH9120 UART-to-Ethernet module:
  - CH9120 RXD ← RP GP21 (UART1 TX)
  - CH9120 TXD → RP GP20 (UART1 RX)
  - TCPCS → GP17 (low = TCP connected)
  - CFG0 ← GP18 (low = serial configuration mode)
  - RSTI ← GP19 (active low)
- Use `-ch9120` or the `rp2350-eth` UI board.
- The module speaks a serial command set (`0x57 0xab …`, `0xaa` replies); the
  TinyGo driver is `drivers/ch9120`, and `firmware/ch9120` configures a TCP
  client against the host echo server.
- The emulator's module proxies TCP/UDP client/server sockets to the host
  network stack; TCPCS and the status line are visible in the UI.

## Raspberry Pi Pico 2 W (virtual Wi-Fi)

**This is a fake, not a CYW43439 emulation.** The real board's Infineon
CYW43439 uses a gSPI transport, a firmware/NVRAM download, WLC IOCTLs,
SDPCM/BDC frames and a Bluetooth stack. Emulating that faithfully is out of
scope, and `soypat/cyw43439` firmware does not run here.

What works instead:

- The `pico2w` UI board (and the same wiring as the CH9120 above) attaches a
  host-backed serial netdev.
- `drivers/ch9120.NetDevice` implements a one-socket `netdev.Netdever`
  (`Socket`/`Bind`/`Connect`/`Listen`/`Accept`/`Send`/`Recv`).
- Firmware calls `netdev.UseNetdev` and then uses TinyGo's `net` package
  normally; `firmware/wifi` dials a host TCP echo, writes a line every 500 ms
  and prints each reply as `wifi echo: hello wifi`.
- Bluetooth (LE/Classic) is **not** modelled.

If you need CYW43439 fidelity, that is a separate, firmware-level emulation
project.

## Breadboard W5500

- W5500 on SPI0 CS GP17; use `-eth` or the `pico2-w5500` UI board.
- The stock `tinygo.org/x/drivers/w5500` netdev driver works unchanged.

## WS2812B

- Single-wire GRB, 24 bits per LED, 1.25 µs bits, T0H ~0.4 µs / T1H ~0.8 µs,
  >50 µs reset. TinyGo drives it through PIO.
- Use `-ws2812 GPIO` or the `pico2-ws2812` UI board; the last frame is decoded
  into colours. Reference firmware: `firmware/ws2812`.

## UART over TCP

Any board can expose UART0/UART1 as a TCP server for interactive work. The
headless form is `-serial-tcp 1:127.0.0.1:0` (the assigned port is printed on
stderr); the UI board key is `serial: [{uart: 1}]`.

## Current limitations

- **CYW43439 / Bluetooth:** not emulated; the Pico 2 W uses the virtual Wi-Fi
  described above.
- **Core 1:** works with `-scheduler=cores` (SIO launch, WFE/SEV, tasks on both
  cores). The default TinyGo tasks scheduler is single-core.
- **Interrupt nesting:** exceptions are delivered at IPSR 0; a pending
  higher-priority exception does not preempt the current handler yet.
- **USB host:** the emulated host enumerates the device and pumps CDC; arbitrary
  USB devices are not supported.
- **DMA:** paced transfers and completion interrupts, but no DREQ pacing.
- **High-speed peripheral buses:** UART/SPI/I2C traffic is tapped at the
  controller-model level for the logic analyzer; the emulated GPIO pins do not
  toggle at bus bit rates. PIO/bit-banged traffic is decoded from pins.
- **PSRAM/QSPI:** not modelled.
- **Network tests** depend on host servers and wall-clock timing; see
  [ci.md](ci.md).

## Firmware examples in this repository

| firmware | demonstrates |
|---|---|
| `firmware/blinky` | GPIO and timing |
| `firmware/lcd` | ST7789 over SPI |
| `firmware/touchlcd` | Touch-LCD-2: ST7789, CST816D, QMI8658, ADC, microSD |
| `firmware/piotest` | PIO API conformance (FIFOs, autopush/pull, IRQ, side-set) |
| `firmware/ws2812` | WS2812B strip over PIO |
| `firmware/cores` | `-scheduler=cores` and core 1 |
| `firmware/echo` | USB CDC console input |
| `firmware/uart` | UART output for the logic analyzer |
| `firmware/protocols` | UART/I2C/SPI traffic for the logic analyzer |
| `firmware/serialbridge` | UART1 echoed over TCP |
| `firmware/w5500` | W5500 TCP client/server and UDP (stock driver) |
| `firmware/w6300` | W6300 TCP client/server and UDP |
| `firmware/ch9120` | CH9120 TCP client |
| `firmware/wifi` | virtual Wi-Fi through `netdev` |
