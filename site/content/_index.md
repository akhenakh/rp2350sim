---
title: rp2350sim
geekdocToC: false
---

## RP2350 Simulator

rp2350 is a set of tools to facilitate development on the RP2350 ecosystem.

It provides a CLI tool and a user interface to run, mock periphericals, debug rp2350 program
- Cortex-M33 interpreter checked against QEMU instruction by instruction
- TinyGo and stock Pico SDK C firmware
- GPIO, UART, SPI, I2C, PWM, ADC, PIO, DMA, USB CDC, OTP and QSPI PSRAM
- Scripted Lua/WASM I2C, SPI and UART devices; 1-Wire, DHT, HC-SR04, NMEA GPS
  and MCP2515 CAN
- ST7789, touch, IMU, microSD, W5500/W6300/CH9120, WS2812B and virtual Wi-Fi
- ITM/DWT trace, desktop board picker, scope, logic analyzer, Bus Pirate
  terminals and a DWARF debugger

![Logic tab](./docs/images/logic.png)

## Try it in the browser

The desktop UI also runs as a WebAssembly demo — the same window, no install.
The default demo boots a mate-in-1 chess game on the Touch-LCD-2:

[Open the web demo](demo/?board=touch-lcd2&fw=firmware/tinychess.elf&tab=lcd) ·
[GEEK LCD demo](demo/?board=geek&fw=firmware/lcd.elf&tab=lcd)

It is a demo build: firmware comes from the page, host devices (Ethernet,
bridges, GDB, file picker) are absent, and compute-heavy firmware runs slower
than real time. See the [browser build notes](docs/wasm/) for the numbers.

## Run RP2350 firmware

`rp2350sim` runs stock TinyGo `-target=pico2` and Pico SDK C firmware: a
Cortex-M33 interpreter, the RP2350 chip peripherals, board models and a Shirei
desktop UI with a scope, logic analyzer, Bus Pirate terminals and debugger.

[Read the documentation](docs/)

