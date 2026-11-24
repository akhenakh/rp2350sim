# Bus Pirate targets and fake peripherals

The I2C and SPI TCP bridges let a program outside the emulator act as a
peripheral. The bridge speaks a newline-delimited Bus Pirate subset: the
emulator sends one request token and waits for one response before continuing.

This is enough to fake an EEPROM, a sensor, a flash chip, or a second emulator
instance without writing a new C model.

## Start a bridge

Headless:

```sh
rp2350sim -fw build/busmaster.elf -seconds 5 \
  -i2c-tcp 0:0x50@127.0.0.1:0 \
  -spi-tcp 0:17@127.0.0.1:0
```

- `-i2c-tcp BUS:ADDR[,ADDR...]@HOST:PORT` listens for one I2C target. The
  address is 7-bit (`0x50` becomes the wire bytes `0xA0`/`0xA1`).
- `-spi-tcp BUS:CS@HOST:PORT` listens for one SPI device on that chip select.
- Empty or `127.0.0.1:0` selects a free port; the assigned address is logged as
  `bus bridge listening`.
- Use `=HOST:PORT` instead of `@...` to connect to a device server. In that
  mode the emulator dials the external program.
- `-bus-timeout 1s` bounds one request; `-bus-wait 2s` waits once for the first
  client. A missing client then returns NAK/`0xff` instead of blocking the
  machine.

The UI equivalent is a board with `i2c_tcp`/`spi_tcp`, for example
`pico2-bus`; open the **Bus** tab. The rest of the flags and the UI terminal are
in [peripherals.md](peripherals.md#bus-pirate-i2c-and-spi-targets) and
[ui.md](ui.md).

Common devices are also built in and need no script at all:

```sh
rp2350sim -fw build/blinky.elf -seconds 2 \
  -i2c-preset 0:bme280@0x76 -i2c-preset 0:ssd1306@0x3c \
  -spi-preset 0:w25q16@17
```

`bme280`, `ssd1306` and `mpu6050` are I2C targets; `w25q16` is a 2 MiB SPI NOR
flash. `-list-presets` prints the registry. The same models are selectable in
`boards.yaml` with `i2c_presets`/`spi_presets`, for example the
`pico2-presets` board. `firmware/presets` probes all four (`task run:presets`).

For a scripted or headless session, `--bus-connect` attaches the terminal to
every listener and `--bus-answer kind=TOKENS` queues the device responses:

```sh
rp2350sim-ui --board pico2-bus --tab bus --bus-connect \
  --bus-answer 'i2c=ACK ACK 0x12 0x34' \
  --bus-answer 'spi=0xEF 0x40 0x18' \
  --run 1 --png bus.png build/busmaster.elf
```

## Token reference

| request | meaning | response |
|---|---|---|
| `I2C 0 HELLO 0xA0` | connection banner | none |
| `SPI 0 HELLO 17` | connection banner | none |
| `[0xA0` | I2C start + write address | `ACK` or `NAK` |
| `[0xA1 r:1` | I2C start + read address, one byte | data byte or `NAK` |
| `[0xA1 r:4` | compact read request; the bridge splits it into individual `r:1` requests | one byte per `r:1` |
| `0xNN` | I2C data byte | `ACK` or `NAK` |
| `r:1` | I2C read byte | data byte |
| `[0x9F` | SPI first byte of a CS frame | MISO byte |
| `0xNN` | SPI clocked byte | MISO byte |
| `]` | I2C stop / SPI CS release | none |

An I2C target NAKs an address by answering `NAK`; the controller reports
`ABRT_7B_ADDR_NOACK`. NAKing a data byte reports `ABRT_TXDATA_NOACK`. A read
NAK aborts the transfer instead of returning a phantom `0xFF`.

The compact Bus Pirate spelling is a human transcription. The bridge does not
accept a whole `[0xA0 0x00 [0xA1 r:4]` script as one line: it sends the tokens
the controller actually needs, one at a time, and waits for each answer.

## Fake an I2C EEPROM

`firmware/busmaster` writes a register pointer and reads two bytes from a
24C-style EEPROM at `0x50`. A fake device sees:

```text
< I2C 0 HELLO 0xA0
< [0xA0        > ACK
< 0x00         > ACK
< ]            (no response)
< [0xA1 r:1    > 0x12
< r:1          > 0x34
< ]            (no response)
```

The same exchange in compact Bus Pirate notation is:

```text
[0xA0 0x00 ] [0xA1 r:2 ]
```

A write of two bytes to the same part:

```text
< [0xA0  > ACK
< 0x10   > ACK
< 0x42   > ACK
< ]      (no response)
```

To exercise the read path, start `firmware/busmaster` with an I2C bridge and
either use the UI terminal or `tools/busfake`:

```sh
go build -o build/busfake ./tools/busfake
build/busfake -kind i2c -addr 127.0.0.1:PORT
```

`busfake` starts with `mem[0] = 0x12`, `mem[1] = 0x34`; reading register `0x00`
returns those two bytes. Use `-i2c-addr 0x50` to change the target address and
`-v` to print every request and response.

## Fake an SPI flash

The SPI bridge encloses each transaction between `[` and `]`. The controller
clocks a byte for every SPI transfer, so a three-byte JEDEC ID read appears as
three requests; the device answers with the MISO byte for each clock:

```text
< SPI 0 HELLO 17
< [0x9F   > 0xEF
< 0x9F    > 0x40
< 0x9F    > 0x18
< ]       (no response)
```

The compact Bus Pirate form is `[0x9F r:3]`; the bridge turns that into the
three clocked `0x9F` requests above. A page-program frame is a single CS frame:

```text
< [0x02   > 0xFF
< 0x00    > 0xFF
< 0x00    > 0xFF
< 0x10    > 0xFF
< 0xAA    > 0xFF
< 0xBB    > 0xFF
< ]       (no response)
```

Run the ready-made JEDEC fake against the listening bridge:

```sh
build/busfake -kind spi -addr 127.0.0.1:PORT
```

It returns `EF 40 18` for `0x9F`, matching `firmware/busmaster` and the
`pico2-bus` demo.

## Use the UI terminal

`task ui:bus` starts the `pico2-bus` board paused. Connect the I2C and SPI
terminals, then queue the responses the firmware will ask for:

| bridge | queue |
|---|---|
| I2C `0x50` | `ACK ACK 0x12 0x34` |
| SPI CS GP17 | `0xEF 0x40 0x18` |

Press **Run**. The Bus tab logs requests and answers as they are exchanged.
Because the UI request deadline is 500 ms, queue the answers before running;
the terminal sends a queued token only when the firmware asks for it.

The bundled master expects the EEPROM read to stop and restart between write
and read, so the I2C queue has two ACKs (address and pointer) followed by the
two data bytes. The `]` tokens never consume a response.

## Test with two simulators

A second simulator can implement the same fake device in firmware. Start
`firmware/busdevice`, read its UART addresses, then connect the master to them:

```sh
rp2350sim -fw build/busdevice.elf -seconds 300 \
  -serial-tcp 0:127.0.0.1:0,1:127.0.0.1:0

rp2350sim -fw build/busmaster.elf -seconds 5 \
  -i2c-tcp 0:0x50=127.0.0.1:UART0 \
  -spi-tcp 0:17=127.0.0.1:UART1
```

The device simulator prints `i2c: 12 34` and `spi: ef 40 18` from the master.
The bridge is transport-only: the second simulator is a software peripheral,
not a hardware I2C/SPI target on the emulated bus.
