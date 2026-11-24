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
- ST7789, touch, IMU, microSD, W5500/W6300/CH9120, WS2812B and virtual Wi-Fi
- Desktop board picker, scope, logic analyzer, Bus Pirate terminals and a
  DWARF debugger

![Logic tab](./docs/images/logic.png)

## Run RP2350 firmware

`rp2350sim` runs stock TinyGo `-target=pico2` and Pico SDK C firmware: a
Cortex-M33 interpreter, the RP2350 chip peripherals, board models and a Shirei
desktop UI with a scope, logic analyzer, Bus Pirate terminals and debugger.

[Read the documentation](docs/)

