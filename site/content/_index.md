---
title: rp2350sim
layout: hextra-home
toc: false
---

{{< hextra/hero-headline >}}
Run RP2350 firmware.
{{< /hextra/hero-headline >}}

{{< hextra/hero-subtitle >}}
An RP2350 / Pico 2 emulator for stock TinyGo and Pico SDK C firmware, with a
desktop UI, logic analyzer, PSRAM, debugger and board models.
{{< /hextra/hero-subtitle >}}

{{< hextra/hero-badge link="docs" >}}
Read the documentation
{{< /hextra/hero-badge >}}

{{< hextra/hero-button text="Documentation" link="docs" >}}

## What is included

- Cortex-M33 interpreter checked against QEMU instruction by instruction
- TinyGo `-target=pico2` and stock Pico SDK C firmware
- GPIO, UART, SPI, I2C, PWM, ADC, PIO, DMA, USB CDC, OTP, QSPI PSRAM
- ST7789, touch, IMU, microSD, W5500/W6300/CH9120, WS2812B and virtual Wi-Fi
- A Shirei desktop UI with board picker, scope, logic analyzer, Bus Pirate
  terminals and a DWARF debugger
