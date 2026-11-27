# `boards.yaml` reference

The desktop UI builds its board list from `cmd/simui/boards.yaml`. Each board
describes the RP2350 package and the external devices wired to it; the UI adds a
tab for every device that exists. This page is the complete field reference.
For the built-in boards and their wiring, see [boards.md](boards.md).

`boards.yaml` is **UI-only**. The headless runner attaches devices with flags
(`-i2c-preset`, `-eth`, `-audio-pwm`, …); see
[peripherals.md](peripherals.md) for that mapping.

## Loading and overriding

```text
cmd/simui/boards.yaml             embedded in the binary
RP2350SIM_BOARDS=/path/boards.yaml   replaces the embedded list
```

After editing the embedded file, rebuild the UI. For experiments, point
`RP2350SIM_BOARDS` at a copy:

```sh
RP2350SIM_BOARDS=./my-boards.yaml rp2350sim-ui --list-boards
RP2350SIM_BOARDS=./my-boards.yaml rp2350sim-ui
```

The file has one `boards:` list. `id` is required and must be unique; `name`
defaults to the id. `dir`/`image` paths in `sd` are expanded for `~` and
environment variables and, when relative, resolved against the directory of the
custom `boards.yaml`.

```yaml
boards:
  - id: pico2
    name: Raspberry Pi Pico 2
    description: RP2350A · USB CDC console · on-board LED on GP25
```

## Board-level fields

| field | type | default | meaning |
|---|---|---|---|
| `id` | string | required | picker id, unique |
| `name` | string | `id` | display name |
| `description` | string | empty | picker/toolbar subtitle |
| `package` | `qfn60` \| `qfn80` | `qfn60` | RP2350A (30 GPIOs) or RP2350B (48 GPIOs) |
| `hstx` | bool | `false` | enable the DVI/HSTX tab |
| `scope` | list of GPIOs | `[0, 1]` | default scope channels, at most 16 |
| `inputs` | list | empty | interactive Board-tab controls |
| `analog` | list | empty | generated analog signals |
| `logic` | mapping | empty | logic-analyzer protocol hints |

Everything else is a device mapping; every key is optional and an absent device
has no tab.

| device key | tab | peripheral |
|---|---|---|
| `lcd` | LCD | ST7789 over SPI |
| `touch` | LCD + Sensors | CST816D touch on I2C |
| `imu` | Sensors | QMI8658 on I2C |
| `battery` | Sensors | resistor-divider battery ADC |
| `sd` | Sensors | microSD over SPI |
| `ethernet` | Ethernet | W5500, W6300 or CH9120 |
| `wifi` | Wi-Fi | virtual serial netdev |
| `ws2812` | LEDs | WS2812B pin decoder |
| `audio` | Audio | I2S, PDM or PWM input |
| `midi` | MIDI | USB or UART MIDI |
| `psram` | Board status | QSPI PSRAM on QMI CS1 |
| `serial` | Console | UART TCP bridge |
| `i2c_tcp` / `spi_tcp` | Bus | Bus Pirate token bridge |
| `i2c_presets` / `spi_presets` | Bus | built-in device models |
| `i2c_plugins` / `spi_plugins` | Bus | Lua or WASM device files |
| `serial_plugins` | Console | Lua or WASM UART device files |
| `onewire` | Board | Timed 1-Wire devices (DS18B20) |
| `dht` | Board | Timed DHT11/DHT22 sensors |
| `ultrasonic` | Board | Timed HC-SR04 sensors |
| `gps` | Board / Console | NMEA 0183 source on a UART |
| `can` | Board / Console | MCP2515 CAN controller and peer |

The Board, Scope, Logic, OTP, Debug, Profile and Console tabs always exist.

## `lcd`

An ST7789 SPI panel. The panel is drawn into a full GRAM and exported as a
framebuffer; `width`/`height` are the logical panel size after rotation.

| field | type | default | meaning |
|---|---|---|---|
| `spi` | 0 \| 1 | required | SPI controller |
| `cs` | GPIO | required | chip select |
| `dc` | GPIO | required | data/command |
| `rst` | GPIO | required | reset |
| `width` | int | required | logical width |
| `height` | int | required | logical height |
| `rotation` | 0 \| 90 \| 180 \| 270 | `0` | logical rotation |
| `backlight` | GPIO | none | PWM/backlight pin |

```yaml
lcd:
  spi: 1
  cs: 9
  dc: 8
  rst: 12
  width: 240
  height: 135
  rotation: 90
  backlight: 25
```

## `touch`

A CST816D capacitive controller on I2C. `width`/`height` must match the LCD's
**native** geometry after its rotation; a 90/270 LCD swaps the dimensions.

| field | type | default | meaning |
|---|---|---|---|
| `bus` | 0 \| 1 | required | I2C controller |
| `addr` | 7-bit | required | controller address |
| `int` | GPIO | required | interrupt |
| `rst` | GPIO | none | reset; may share the LCD reset |
| `width` | int | required | native width |
| `height` | int | required | native height |
| `rotation` | 0 \| 90 \| 180 \| 270 | `0` | coordinate rotation |

```yaml
lcd:
  spi: 0
  cs: 17
  dc: 16
  rst: 20
  width: 320
  height: 240
  rotation: 90
touch:
  bus: 0
  addr: 0x15
  int: 29
  rst: 20          # shared with the LCD reset
  width: 240
  height: 320
  rotation: 90
```

## `imu`

A QMI8658 accelerometer/gyroscope on I2C.

| field | type | default | meaning |
|---|---|---|---|
| `bus` | 0 \| 1 | required | I2C controller |
| `addr` | 7-bit | required | controller address |
| `int` | GPIO | required | data-ready interrupt |
| `int2` | GPIO | none | second interrupt |

```yaml
imu:
  bus: 0
  addr: 0x6b
  int: 14
```

## `battery`

A resistor-divider battery monitor on one of the ADC pins.

| field | type | default | meaning |
|---|---|---|---|
| `pin` | 26–29 | required | ADC input (the loader's ADC range) |
| `divider` | float | `1` | resistor divider ratio |
| `volts` | float | `3.9` | initial battery voltage |

```yaml
battery:
  pin: 28
  divider: 3
  volts: 3.9
```

## `sd`

A microSD card on a hardware SPI bus. Use either a host directory or a raw FAT16
image, not both. A directory is packed at startup and firmware writes are
mirrored back.

| field | type | default | meaning |
|---|---|---|---|
| `spi` | 0 \| 1 | required | SPI controller |
| `cs` | GPIO | required | chip select |
| `dir` | path | none | host directory backing the card |
| `image` | path | none | raw FAT16 image |

```yaml
sd:
  spi: 1
  cs: 25
  dir: sdcards/my-board
```

## `ethernet`

Three supported models. `kind` defaults to `w5500`.

| field | used by | meaning |
|---|---|---|
| `kind` | all | `w5500`, `w6300` or `ch9120` |
| `spi`, `cs` | w5500/w6300 | SPI controller and chip select |
| `uart` | ch9120 | UART controller (0 or 1) |
| `cfg0`, `rst`, `tcpcs` | ch9120 | CH9120 configuration/reset/TCP-select pins |

```yaml
ethernet:
  kind: w6300
  spi: 0
  cs: 17
```

```yaml
ethernet:
  kind: ch9120
  uart: 1
  cfg0: 18
  rst: 19
  tcpcs: 17
```

The CH9120 UART is claimed for the lifetime of the board; another device cannot
use the same controller.

## `wifi`

The Pico 2 W virtual Wi-Fi netdev. It uses the same host-backed serial protocol
as the CH9120; the CYW43439 radio and Bluetooth are not emulated.

| field | type | default | meaning |
|---|---|---|---|
| `uart` | 0 \| 1 | required | transport UART |
| `cfg0` | GPIO | required | configuration pin |
| `rst` | GPIO | required | reset |
| `tcpcs` | GPIO | required | TCP chip select |

```yaml
wifi:
  uart: 1
  cfg0: 18
  rst: 19
  tcpcs: 17
```

## `ble`

Attaches the virtual CYW43 BLE controller. The firmware must be an ELF with a
symbol table; the emulator binds the CYW43 HCI symbols by name.

| field | type | default | meaning |
|---|---|---|---|
| `addr` | string | `C0:DE:12:34:56:78` | advertised peer address |
| `host_wake` | GPIO | `24` | CYW43 HOST_WAKE pin (Pico W wiring) |
| `seed` | int | `42` | controller PRNG seed |

```yaml
ble:
  addr: "C0:DE:12:34:56:78"
  host_wake: 24
  seed: 42
```

See [ble.md](ble.md) for the stock firmware, flags and pcapng capture.

## `ws2812`

A WS2812B data line decoded from the PIO/GPIO wire timing.

| field | type | default | meaning |
|---|---|---|---|
| `pin` | GPIO | required | data line |

```yaml
ws2812:
  pin: 0
```

## `audio`

An audio decoder for the Audio tab and WAV export. Pins must be distinct and
must not conflict with another device pin.

| field | used by | meaning |
|---|---|---|
| `kind` | all | `i2s`, `pdm` or `pwm` |
| `bclk`, `lrclk`, `data` | i2s | bit clock, word clock, data |
| `data`, `clock` | pdm | data and clock |
| `pin` | pwm | PWM/GPIO audio pin |
| `bits` | all | sample bits, default 16 (1–32) |
| `rate` | all | sample rate, default 48000 |
| `decimation` | pdm | decimation ratio, default 64 |

```yaml
audio:
  kind: i2s
  bclk: 0
  lrclk: 1
  data: 2
  rate: 48000
  bits: 16
```

## `midi`

| field | type | default | meaning |
|---|---|---|---|
| `kind` | `usb` \| `uart` | required | MIDI transport |
| `uart` | 0 \| 1 | for `uart` | UART controller |

```yaml
midi:
  kind: uart
  uart: 1
```

## `psram`

QSPI PSRAM on QMI chip select 1.

| field | type | default | meaning |
|---|---|---|---|
| `size_mb` | 1, 2, 4, 8, 16 | required | device size |
| `cs` | GPIO | required | QMI chip select |
| `latency` | int | `0` | extra cycles charged per access |

```yaml
psram:
  size_mb: 8
  cs: 8
  latency: 6
```

## `analog`

Generated analog sources for the Scope tab and the ADC. The same definitions
work in headless snapshots.

| field | type | default | meaning |
|---|---|---|---|
| `pin` | GPIO | required | driven pin |
| `wave` | `sine`, `square`, `triangle`, `ramp`, `noise` | `sine` | waveform |
| `hz` | float | `1000` | frequency |
| `amplitude` | float | `1.65` | peak amplitude in volts; defaults with offset |
| `offset` | float | `1.65` | DC offset in volts; defaults with amplitude |

Amplitude and offset both default to 1.65 only when both are omitted. A noise
source ignores `hz`.

```yaml
analog:
  - { pin: 26, wave: sine, hz: 1000, amplitude: 1.65, offset: 1.65 }
  - { pin: 27, wave: ramp, hz: 200, amplitude: 1.65, offset: 1.65 }
  - { pin: 28, wave: noise, amplitude: 1.0, offset: 1.65 }
  - { pin: 29, wave: square, hz: 500, amplitude: 1.65, offset: 1.65 }
```

## `inputs`

Interactive controls on the Board tab. They drive the same pin model the
firmware reads, survive reset, and can be scripted with
`--input ID=on|off|VOLTS|+N|-N`.

Common fields:

| field | type | default | meaning |
|---|---|---|---|
| `kind` | see below | required | control kind |
| `id` | string | required | unique id used by `--input` |
| `label` | string | upper-cased id | panel label |
| `active_low` | bool | `true` | invert the electrical level |

Kinds and their fields:

| kind | fields | notes |
|---|---|---|
| `button` (aliases: `btn`, `pushbutton`) | `pin`, `initial` | momentary; `initial` sets the resting state |
| `switch` (aliases: `slide`, `toggle`) | `pin`, `initial` | latching |
| `encoder` (aliases: `rotary`) | `pin_a`, `pin_b` | two distinct quadrature lines |
| `adc` (aliases: `pot`, `slider`) | `pin` 26–29, `volts`, `min`, `max`, `step` | `min`/`max` default 0/3.3 V, `step` 0.01, `volts` mid-scale |
| `bootsel` | — | at most one; uses QSPI_CSN |

```yaml
inputs:
  - { kind: button, id: btn1, label: BTN 1, pin: 15 }
  - { kind: switch, id: sw1, label: SW 1, pin: 14, initial: false }
  - { kind: encoder, id: enc1, label: ENC 1, pin_a: 12, pin_b: 13 }
  - { kind: adc, id: pot1, label: POT 1, pin: 26, volts: 1.65, step: 0.05 }
  - { kind: bootsel, id: bootsel, label: BOOTSEL }
```

If no `bootsel` input is declared, one is added automatically for every board.
The `bootsel` id is reserved when it is not the BOOTSEL kind.

## `logic`

Hints for the Logic tab decoders. Pins are observed, not claimed by a device;
they must still be inside the selected package. `miso` and `cs` are optional for
SPI. The 2 MHz sampler runs while the Logic tab is open or a trigger is armed,
so a board can declare the hints without slowing the other tabs.

```yaml
logic:
  uart: { bus: 0, tx: 0, rx: 1, baud: 115200 }
  i2c:  { bus: 0, sda: 4, scl: 5 }
  spi:  { bus: 0, sck: 18, mosi: 19, miso: 16, cs: 17 }
```

| protocol | fields |
|---|---|
| `uart` | `bus` 0/1, `tx`, `rx`, `baud` (positive) |
| `i2c` | `bus` 0/1, `sda`, `scl` |
| `spi` | `bus` 0/1, `sck`, `mosi`, optional `miso`, optional `cs` |

## `serial`

Expose a hardware UART over TCP for the Console tab.

| field | type | default | meaning |
|---|---|---|---|
| `uart` | 0 \| 1 | required | hardware UART |
| `listen` | address | `127.0.0.1:0` | TCP listen address |

```yaml
serial:
  - uart: 1
```

An empty `listen` asks the kernel for a free port, shown in the Console tab.

## `i2c_tcp` and `spi_tcp`

Expose a bus to an external Bus Pirate token client. `listen` and `connect` are
mutually exclusive; with neither, the bridge listens on `127.0.0.1:0`.

```yaml
i2c_tcp:
  - bus: 0
    addrs: [0x50]
    listen: 127.0.0.1:0
spi_tcp:
  - bus: 0
    cs: 17
    connect: 127.0.0.1:39480
```

| key | fields |
|---|---|
| `i2c_tcp` | `bus` 0/1, `addrs` (at least one 7-bit address), `listen` xor `connect` |
| `spi_tcp` | `bus` 0/1, `cs`, `listen` xor `connect` |

Every address is registered on the emulated I2C bus, so duplicate addresses and
conflicts with `touch`, `imu` or presets are rejected.

## `i2c_presets` and `spi_presets`

Attach a built-in device model directly, with no TCP bridge.

```yaml
i2c_presets:
  - { bus: 0, preset: bme280, addr: 0x76 }
  - { bus: 0, preset: ssd1306, addr: 0x3c }
  - { bus: 0, preset: mpu6050, addr: 0x68 }
spi_presets:
  - { bus: 0, cs: 17, preset: w25q16 }
```

| key | fields |
|---|---|
| `i2c_presets` | `bus` 0/1, `preset`, `addr` (`0` selects the model's first address) |
| `spi_presets` | `bus` 0/1, `cs`, `preset` |

Built-in presets:

| name | kind | addresses |
|---|---|---|
| `bme280` | I2C | `0x76`, `0x77` |
| `mpu6050` | I2C | `0x68`, `0x69` |
| `ssd1306` | I2C | `0x3c`, `0x3d` |
| `w25q16` | SPI | any CS |

Run `rp2350sim -list-presets` for the current list.

## `onewire`

A timed 1-Wire bus on one GPIO. The model drives the line through the chip's
pin state and schedules slots/deadlines on the virtual clock.

```yaml
onewire:
  - { pin: 6, type: ds18b20, temperature: 22.5 }
```

| key | type | default | meaning |
|---|---|---|---|
| `pin` | GPIO | required | 1-Wire data line |
| `type` | `ds18b20` | `ds18b20` | device model |
| `temperature` | float | `22.5` | reported Celsius value |

Only one device per pin is supported today; duplicate pins are a validation
error.

## `dht` and `ultrasonic`

```yaml
dht:
  - { pin: 6, kind: dht22, humidity: 45, temperature: 23 }
ultrasonic:
  - { trig: 6, echo: 7, distance_cm: 100 }
```

| key | type | default | meaning |
|---|---|---|---|
| `dht[].pin` | GPIO | required | data line |
| `dht[].kind` | `dht11` \| `dht22` | `dht22` | protocol |
| `dht[].humidity` | float | `45` | percent |
| `dht[].temperature` | float | `23` | Celsius |
| `ultrasonic[].trig` | GPIO | required | trigger output |
| `ultrasonic[].echo` | GPIO | required | echo input |
| `ultrasonic[].distance_cm` | float | `100` | measured distance |

## `gps`

```yaml
gps:
  uart: 1
  lat: 45.5017
  lon: -73.5673
  altitude: 82
  speed_knots: 3.2
  course: 270
  satellites: 9
  fix: true
  rate_ms: 1000
```

| key | type | default | meaning |
|---|---|---|---|
| `uart` | 0 \| 1 | required | output UART |
| `lat`, `lon` | float | `45.5017`, `-73.5673` | position |
| `altitude` | float | `82` | metres |
| `speed_knots` | float | `0` | RMC speed |
| `course` | float | `0` | RMC course |
| `satellites` | int | `9` | GGA satellite count |
| `fix` | bool | `true` | GGA/RMC fix state |
| `rate_ms` | int | `1000` | sentence period, minimum 50 |

## `can`

```yaml
can:
  bus: 0
  cs: 17
  int: 20
  peer_id: 0x123
  reply_id: 0x124
```

| key | type | default | meaning |
|---|---|---|---|
| `bus` | 0 \| 1 | required | SPI controller |
| `cs` | GPIO | required | MCP2515 chip select |
| `int` | GPIO | `-1` | optional active-low interrupt |
| `peer_id` | int | `0x123` | peer request ID |
| `reply_id` | int | `peer_id + 1` | peer response ID |

The peer increments the data bytes of a matching request. Classic CAN only;
CAN FD is not modelled yet.

## `i2c_plugins`, `spi_plugins` and `serial_plugins`

Scripted device models are files: a `.lua` script runs in the bundled Lua
wasm runtime, or a `.wasm` module implements the device ABI directly. The
complete plugin guide is [plugins.md](plugins.md).

```yaml
i2c_plugins:
  - bus: 0
    addr: 0x76
    name: bme280
    file: plugins/examples/bme280.lua      # desktop
    url: plugins/examples/bme280.lua       # browser
    config: { seed: 1 }

spi_plugins:
  - { bus: 0, cs: 17, file: plugins/examples/w25q16.lua }

serial_plugins:
  - uart: 1
    file: plugins/examples/uart-modem.lua
    config: { baud: 115200 }
```

| key | fields |
|---|---|
| `i2c_plugins` | `bus` 0/1, `addr` (7-bit), `file` and/or `url`, optional `name`, `config` |
| `spi_plugins` | `bus` 0/1, `cs`, `file` and/or `url`, optional `name`, `config` |
| `serial_plugins` | `uart` 0/1, `file` and/or `url`, optional `name`, `config`, `pins` |

`file` is the desktop path and `url` is the browser source; provide the one
your target needs. A relative `file` is resolved against the custom
`boards.yaml` directory. `config` is a Lua table for `.lua` files and JSON for
`.wasm` files.

`pins` maps logical plugin lines to GPIOs, for example
`{ irq: 20, reset: 21 }`; the plugin can read/write them through its host API.
Plugin addresses, chip selects and UARTs follow the same conflict rules as
presets and bridges.

## Validation rules

The loader rejects a board file at startup when an id is missing or duplicated,
or when any of these conflicts exist:

- **Pins** must be in the selected package (0–29 for `qfn60`, 0–47 for
  `qfn80`), and device pins cannot be reused. The LCD and touch resets may share
  one GPIO.
- **SPI** bus must be 0 or 1. Two devices may share a bus only with different
  chip selects; one GPIO cannot be a chip select on both controllers.
- **I2C** must be a 7-bit address (0x08–0x77) and unique per bus across touch,
  IMU, TCP bridges and presets. The bus must be 0 or 1.
- **UART** must be 0 or 1. The CH9120 and virtual Wi-Fi claim their UART.
- **Touch geometry** must match the LCD's native geometry after rotation.
- **Audio** pins must be distinct and must not be another device pin; `bits`
  is 1–32, `rate` and `decimation` are positive (defaults 48000 and 64).
- **PSRAM** `size_mb` must be 1, 2, 4, 8 or 16; `latency` cannot be negative.
- **Scope** pins must be unique and inside the package; at most 16.
- **Inputs** need unique ids and non-conflicting pins. ADC pins are the
  loader's 26–29 range. There is at most one `bootsel`; the id is reserved when
  it is another kind.
- **Plugins** need a `file` or `url`, a valid slot, and cannot reuse an I2C
  address, SPI chip select or UART already claimed by another device.

Unknown YAML keys are ignored by the parser, so use the exact names above.

## Complete example

A custom RP2350A board with an LCD, shared-reset touch, IMU, SD card, BME280,
signal sources, controls and logic hints:

```yaml
boards:
  - id: custom-lcd
    name: Custom RP2350A LCD board
    description: ST7789 + touch + IMU + SD + BME280 + scope sources
    package: qfn60
    scope: [0, 1, 26, 27]

    lcd:
      spi: 1
      cs: 9
      dc: 8
      rst: 12
      width: 240
      height: 135
      rotation: 90
      backlight: 25
    touch:
      bus: 0
      addr: 0x15
      int: 29
      rst: 12          # shared LCD reset
      width: 135
      height: 240
      rotation: 90
    imu:
      bus: 0
      addr: 0x6b
      int: 14
    battery:
      pin: 28
      divider: 3

    sd:
      spi: 0
      cs: 23
      dir: sdcards/custom-lcd

    i2c_presets:
      - { bus: 0, preset: bme280, addr: 0x76 }
    i2c_plugins:
      - { bus: 0, addr: 0x77, file: plugins/examples/bme280.lua }
      - { bus: 0, addr: 0x68, file: plugins/examples/mpu6050.lua }
    ws2812:
      pin: 4

    analog:
      - { pin: 27, wave: sine, hz: 1000, amplitude: 1.65, offset: 1.65 }

    inputs:
      - { kind: button, id: btn1, label: BTN 1, pin: 15 }
      - { kind: switch, id: sw1, label: SW 1, pin: 16 }
      - { kind: encoder, id: enc1, pin_a: 17, pin_b: 18 }
      - { kind: adc, id: pot1, pin: 26, volts: 1.65, step: 0.05 }

    logic:
      uart: { bus: 0, tx: 0, rx: 1, baud: 115200 }
```

Start the UI with `RP2350SIM_BOARDS=./custom-boards.yaml rp2350sim-ui`, or copy
the entry into `cmd/simui/boards.yaml` and rebuild.
