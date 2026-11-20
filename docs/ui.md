# Desktop UI

The UI is a separate Go module (`cmd/simui/`) so GUI dependencies never enter
the emulator build. It shows the selected board, live GPIO, LCD, Ethernet/Wi-Fi
state, touch/IMU/battery sensors, a console, an oscilloscope and a logic
analyzer.

```sh
cd cmd/simui
go run . --board geek ../../path/to/firmware.elf
```

The window is 1280×820 by default; `--width`/`--height` change it.
**Cmd+Q** on macOS or **Ctrl+Q** elsewhere quits the UI.

## Startup: the board picker

The UI opens on a **scrollable list of boards** loaded from
`cmd/simui/boards.yaml`
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
| `--version` | print the version and exit |
| `--tab NAME` | initial tab: `board`, `lcd`, `eth`/`wifi`, `ws`, `sensors`, `scope`, `logic`, `otp`, `console` |
| `--width`, `--height` | window size in points |
| `--run SECONDS` | advance virtual time at startup; a live window keeps running, `--png` snapshots stay frozen |
| `--paused` | load the firmware but do not execute until Run |
| `--png PATH` | render one settled frame and exit (headless) |
| `--flash PATH` | raw flash backing image; self-updates are written back here |
| `--touch x,y` | inject a held touch at native panel coordinates |
| `--touch-demo` | move a synthetic touch around the panel while the UI runs |
| `--scope-zoom N`, `--scope-range V` | initial scope time zoom / full-scale volts |

The toolbar shows the selected board and a **Board…** button to return to the
picker. Changing boards reloads the current firmware onto the new hardware.

## `boards.yaml`

A board declares the devices it has. Every key is optional; the UI creates a
tab only when the device exists.

| key | fields |
|---|---|
| `lcd` | `spi`, `cs`, `dc`, `rst`, `width`, `height`, `rotation`, `backlight` |
| `touch` | `bus`, `addr`, `int`, `rst`, `width`, `height`, `rotation` |
| `imu` | `bus`, `addr`, `int`, `int2` |
| `battery` | `pin` (26-29), `divider`, `volts` |
| `sd` | `spi`, `cs`, `dir` (host directory) or `image` (raw FAT image) |
| `ethernet` | `kind` (`w5500`/`w6300`/`ch9120`), `spi`, `cs`, or the CH9120 pins |
| `ws2812` | `pin` |
| `analog` | list of `{pin, wave, hz, amplitude, offset}` |
| `logic` | `{uart, i2c, spi}` pins the analyzer observes |
| `serial` | list of `{uart, listen}` TCP bridges |
| `wifi` | `{uart, cfg0, rst, tcpcs}` virtual netdev |

### Sensors tab and touch

Boards with `touch`, `imu`, `battery` or `sd` get a **Sensors** tab showing the
live touch point, acceleration/rate, die temperature, battery voltage, PWM
backlight duty and card size. Touching the card state is read-only.

On a touch board, clicking or dragging the **LCD** panel injects a finger at the
corresponding native panel coordinate: the UI maps the displayed (rotated)
frame back through the LCD `rotation`. Release injects a lift.

### SD card directory

`sd.dir` backs the emulated card with a real host directory. Files are packed
into FAT16 at startup; firmware reads and writes are mirrored back after they
settle. The Sensors tab shows the directory path. The distributed boards use
`sdcards/geek` and `sdcards/touch-lcd2` relative to the UI's working
directory. Edit `boards.yaml` or use a custom board file to point elsewhere.

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
    sd: { spi: 0, cs: 23 }

  - id: touch-lcd2
    name: Waveshare RP2350-Touch-LCD-2
    lcd: { spi: 0, cs: 17, dc: 16, rst: 20, width: 240, height: 320,
           rotation: 90, backlight: 15 }
    touch: { bus: 0, addr: 0x15, int: 29, rst: 20, width: 240, height: 320,
             rotation: 90 }
    imu: { bus: 0, addr: 0x6b, int: 14 }
    battery: { pin: 28, divider: 3, volts: 3.9 }
    sd: { spi: 1, cs: 25 }

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
| `i2c_tcp` | `{bus, addrs, listen | connect}` | I2C TCP device (Bus tab) |
| `spi_tcp` | `{bus, cs, listen | connect}` | SPI TCP device (Bus tab) |

`listen` defaults to `127.0.0.1:0` (a free port). Two devices cannot use the
same SPI bus.

## Tabs

- **Board** — a stylised Pico 2 header diagram and the live GPIO table
  (function, direction, level). Click a GPIO chip in the scope to add it.
  Panels size themselves to the window: the board card list widens and the LCD
  view scales with the window instead of sitting at a fixed size. After a
  firmware self-update and reset, a banner names the flash image the session is
  now running from and shows its boot number and entry point; the status line
  replaces the initial ELF name with `(flash)`.
- **LCD** — the ST7789 framebuffer, only for boards with `lcd`.
- **Ethernet / Wi-Fi** — the guest identity (IP, mask, gateway, MAC), one row
  per socket and a live log for W5500, W6300, CH9120 or the virtual Wi-Fi
  transport. A listening socket shows both the emulated device address and the
  **host address to connect to** (for example `connect to 127.0.0.1:9002`), so
  the firmware can be exercised with `nc` or a browser.
- **LEDs** — the last WS2812B frame as large swatches with their RGB values.
- **Scope** — see below.
- **Logic** — protocol decoders and waveforms; **Save trace** writes
  `logic-analyze-trace-<date>-<board>.txt` in the current directory with the
  raw pin samples and the decoded UART/I2C/SPI/USB sections.
- **OTP** — the RP2350 OTP array: 64 pages of 64 rows, each page showing its
  programmed-row count and effective secure/non-secure lock. Click a page to
  inspect its rows (data, raw 24-bit value and ECC status). The panel follows
  the last row the firmware programmed until you pick a page. Nothing is
  persistent: seed rows with the runner's `-otp` flag or dump them with
  `-otp-dump`.
- **Bus** — the I2C/SPI TCP device bridges. Select a listening bridge and
  **Connect terminal** to act as the device: the panel shows the Bus Pirate
  request tokens and sends the ACK/NAK/data responses you type. Connect-mode
  bridges are driven by their external server instead.
- **Console** — USB CDC + UART output. Click it to give it keyboard focus;
  typed text and Enter/Backspace/Tab/arrows are forwarded to the firmware.
  UART-over-TCP and bus-bridge status lines are listed here.

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

## I2C/SPI over TCP

Add Bus Pirate devices to a board:

```yaml
i2c_tcp:
  - bus: 0
    addrs: [0x50, 0x51]
    # listen: 127.0.0.1:12402   (omit for a random free port)
spi_tcp:
  - bus: 0
    cs: 17
    # listen: 127.0.0.1:12403
```

The `pico2-bus` board is the ready-made demo (I2C 0x50 and SPI0 CS GP17).
`task ui:bus` opens it on the Bus tab **paused** with the `busmaster` firmware.
Connect the terminal for **each** bridge you want to answer (the I2C and SPI
terminals stay open independently), queue the responses, then press **Run**.
Starting paused is what avoids the 500 ms bridge timeout: queue first, run
afterwards. Without `--paused`, use Reset after queueing. `task ui:bus:png` renders the
tab headlessly to `build/ui-bus.png`.
Bridges listen by default; use `connect: host:port` instead to dial a device
server. Address and chip-select conflicts with LCD, touch, IMU, SD and
Ethernet devices are rejected when the board loads.

Open the **Bus** tab, pick a bridge and **Connect terminal**. Each bridge keeps
its own terminal, so the I2C and SPI devices can both be armed. The terminal is
the device side: requests appear as Bus Pirate tokens such as

```text
< [0xA0
< 0x00
< [0xA1 r:1
< r:1
< ]
```

and you answer with `ACK`, `NAK`, or hex bytes. Responses are queued (the log
shows `+ queued ...` immediately), so you can type `0xEF 0x40 0x18` before a
three-byte SPI read starts; each whitespace token answers one request. The
terminal only writes an answer when the firmware asks; if the log stops at
`HELLO`, press **Reset firmware** in the Bus tab (or Reset in the toolbar).
A request has a 500 ms deadline, so queue the answers before running; `task
ui:bus` starts paused for exactly this reason.
For the bundled `busmaster`, queue `ACK ACK 0x12 0x34` for the I2C exchange
and `0xEF 0x40 0x18` for the SPI exchange. The bridge timeout is 500 ms in the UI; if nothing
is queued the request returns NAK/`0xff` and the machine keeps running. The
listener and terminal survive firmware reloads, so connect first, queue the
answers, then reset.

Connect-mode bridges have no terminal in the UI: their external server is the
device, exactly like the CLI `=host:port` form. See `docs/cli.md` for the full
token protocol and the two-simulator test.

## Virtual Wi-Fi

The `pico2w` board is a **fake**, not a CYW43439 emulation: the radio's gSPI
protocol, firmware download and Bluetooth stack are out of scope. The board
exposes a host-backed serial netdev via the CH9120 transport, and firmware uses
`drivers/ch9120.NetDevice` with `netdev.UseNetdev` so TinyGo's `net` package
works. See [boards.md](boards.md).

## Headless snapshots

```sh
cd cmd/simui
go run . --board geek --tab lcd --run 0.6 --png ../../ui.png ../../firmware.elf
go run . --board pico2-analog --tab scope --run 0.02 --png ../../scope.png
go run . --board pico2-analog --tab logic --run 0.05 --png ../../logic.png ../../build/protocols.elf
go run . --board touch-lcd2 --tab sensors --touch-demo --width 1920 --height 1080 \
  --png ../../sensors.png ../../build/touchlcd.elf
go run . --board pico2 --flash /tmp/ota-flash.bin --run 0.5 --png ../../ota.png \
  ../../firmware/c/build/ota.elf
```

`task ui:ota` opens the same setup live; `task ui:ota-png` writes
`build/ui-ota.png` for a quick visual check of the self-update banner.

`--run` advances virtual time synchronously (the UI does not step in the
background in headless mode), making snapshots deterministic. The system font
scan is awaited before rendering.

## Keyboard

- Console focus: click the console panel. Text and special keys are forwarded
  to the firmware's USB CDC and UART0.
- The path text field owns typing while focused; the console does not steal it.
- Tab moves focus between focusable widgets.
