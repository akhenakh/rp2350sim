# Desktop UI

The UI is a separate Go module (`ui/`) so GUI dependencies never enter the
emulator build. It shows the selected board, live GPIO, LCD, Ethernet/Wi-Fi
state, a console, an oscilloscope and a logic analyzer.

```sh
cd ui
go run . --board geek ../path/to/firmware.elf
```

The window is 1280×820 by default; `--width`/`--height` change it.

## Startup: the board picker

The UI opens on a **scrollable list of boards** loaded from `ui/boards.yaml`
(embedded). Each card shows the board name, description and device badges.
Clicking a card attaches that board's devices to the emulated chip and opens
the matching tabs.

Override the list without rebuilding:

```sh
RP2350SIM_BOARDS=/path/to/boards.yaml go run .
```

Other startup options:

| flag | meaning |
|---|---|
| `--board ID` | skip the picker and select a board (see `--list-boards`) |
| `--list-boards` | print board IDs, names and badges, then exit |
| `--tab NAME` | initial tab: `board`, `lcd`, `eth`/`wifi`, `ws`, `scope`, `logic`, `console` |
| `--width`, `--height` | window size in points |
| `--run SECONDS` | advance virtual time before rendering/using the UI |
| `--png PATH` | render one settled frame and exit (headless) |
| `--scope-zoom N`, `--scope-range V` | initial scope time zoom / full-scale volts |

The toolbar shows the selected board and a **Board…** button to return to the
picker. Changing boards reloads the current firmware onto the new hardware.

## `boards.yaml`

A board declares the devices it has. Every key is optional; the UI creates a
tab only when the device exists.

```yaml
boards:
  - id: pico2
    name: Raspberry Pi Pico 2
    description: RP2350A · USB CDC console · on-board LED on GP25
    scope: [0, 1]
    logic:
      uart: { tx: 0, rx: 1, baud: 115200 }

  - id: geek
    name: Waveshare RP2350-GEEK
    description: 240×135 ST7789 on SPI1, microSD and I2C header
    scope: [0, 1]
    logic:
      uart: { bus: 0, tx: 0, rx: 1, baud: 115200 }
      i2c: { sda: 28, scl: 29 }
      spi: { bus: 1, sck: 10, mosi: 11, cs: 9 }
    lcd: { spi: 1, cs: 9, dc: 8, rst: 12, width: 240, height: 135 }

  - id: rp2350-eth
    name: Waveshare RP2350-ETH (CH9120)
    description: CH9120 UART-to-Ethernet on UART1
    scope: [20, 21]
    ethernet:
      kind: ch9120
      uart: 1
      cfg0: 18
      rst: 19
      tcpcs: 17

  - id: pico2w
    name: Raspberry Pi Pico 2 W (virtual Wi-Fi)
    description: Host-backed virtual Wi-Fi netdev · CYW43439/Bluetooth not emulated
    scope: [20, 21]
    wifi: { uart: 1, cfg0: 18, rst: 19, tcpcs: 17 }
```

### Device keys

| key | fields | effect |
|---|---|---|
| `lcd` | `spi`, `cs`, `dc`, `rst`, `width`, `height` | ST7789 panel, LCD tab |
| `ethernet` | `kind` (`w5500`, `w6300`, `ch9120`), `spi`, `cs`, and for CH9120 `uart`, `cfg0`, `rst`, `tcpcs` | Ethernet tab |
| `wifi` | `uart`, `cfg0`, `rst`, `tcpcs` | virtual Wi-Fi (CH9120 transport), Wi-Fi tab |
| `ws2812` | `pin` | WS2812B tab |
| `scope` | list of GPIO pins | default scope channels |
| `analog` | `{pin, wave, hz, amplitude, offset}` | generated analog signal (scope/ADC demo) |
| `logic` | `uart {bus,tx,rx,baud}`, `i2c {bus,sda,scl}`, `spi {bus,sck,mosi,miso,cs}` | logic analyzer protocols |
| `serial` | `{uart, listen}` | UART TCP bridge (see below) |

`listen` defaults to `127.0.0.1:0` (a free port). Two devices cannot use the
same SPI bus.

## Tabs

- **Board** — a stylised Pico 2 header diagram and the live GPIO table
  (function, direction, level). Click a GPIO chip in the scope to add it.
- **LCD** — the ST7789 framebuffer, only for boards with `lcd`.
- **Ethernet / Wi-Fi** — socket state and a live log for W5500, W6300, CH9120
  or the virtual Wi-Fi transport.
- **LEDs** — the last WS2812B frame as coloured swatches.
- **Scope** — see below.
- **Logic** — see below.
- **Console** — USB CDC + UART output. Click it to give it keyboard focus;
  typed text and Enter/Backspace/Tab/arrows are forwarded to the firmware.
  UART-over-TCP bridges are listed here.

## Oscilloscope

- Up to 8 channels, each with its own colour, 0–3.3 V, 1 MHz sampling and a
  ~16 ms window.
- Click header pins (GP0–GP15, GP26–GP29) to add/remove traces; the legend
  shows the latest voltage.
- **Time Zoom −/+** changes the window from 1× to 64×; **Range −/+** selects
  0.5, 1, 1.65, 2, 3.3 or 5 V full scale; **Reset** restores 1×/3.3 V.
- Analog sources in the board YAML generate waveforms. The `pico2-analog`
  board puts a 1 kHz sine on GP26, which the ADC also reads.

## Logic analyzer

- Board `logic:` protocols are tapped from the hardware models (UART, I2C, SPI)
  or decoded from pin traces as a fallback for PIO/bit-banged signals.
- UART bytes, I2C address/data/ACK phases and SPI MOSI/MISO bytes are listed
  under synthesised waveforms. USB CDC transfers are listed with direction.
- **Trigger**: Off/Any/UART/I2C/SPI captures the trace and decoded lists on the
  next event; **Pause: on** also stops the machine. **Rearm** waits for the
  next event after a frozen capture.

## UART over TCP

Add a bridge to a board:

```yaml
serial:
  - uart: 1
    # listen: 127.0.0.1:12401   (omit for a random free port)
```

The assigned address appears in the Console tab, e.g.
`UART1 tcp 127.0.0.1:39479 · listening`. Connect with `nc 127.0.0.1 39479`;
bytes go to the UART receive FIFO and firmware TX comes back on the same
connection. The listener is kept across firmware resets, so the port remains
stable while the UI is open.

## Virtual Wi-Fi

The `pico2w` board is a **fake**, not a CYW43439 emulation: the radio's gSPI
protocol, firmware download and Bluetooth stack are out of scope. The board
exposes a host-backed serial netdev via the CH9120 transport, and firmware uses
`drivers/ch9120.NetDevice` with `netdev.UseNetdev` so TinyGo's `net` package
works. See [boards.md](boards.md).

## Headless snapshots

```sh
cd ui
go run . --board geek --tab lcd --run 0.6 --png ui.png ../firmware.elf
go run . --board pico2-analog --tab scope --run 0.02 --png scope.png
go run . --board pico2-analog --tab logic --run 0.05 --png logic.png ../build/protocols.elf
```

`--run` advances virtual time synchronously (the UI does not step in the
background in headless mode), making snapshots deterministic. The system font
scan is awaited before rendering.

## Keyboard

- Console focus: click the console panel. Text and special keys are forwarded
  to the firmware's USB CDC and UART0.
- The path text field owns typing while focused; the console does not steal it.
- Tab moves focus between focusable widgets.
