# Desktop UI

`rp2350sim-ui` is a Shirei desktop application that wraps the same emulator as
the headless runner. It starts with a board picker, attaches that board's
devices, and exposes the live machine through tabs: GPIO, LCD, sensors,
Ethernet, LEDs, scope, logic analyzer, OTP, Bus Pirate terminals and the
firmware console.

The UI is a separate Go module (`cmd/simui/`) so GUI dependencies never enter
the emulator build. Download `rp2350sim-ui` from
[GitHub Releases](https://github.com/akhenakh/rp2350sim/releases), or run it
from a checkout:

```sh
cd cmd/simui
go run . --board geek ../../build/lcd.elf
```

The window is 1280×820 by default; `--width`/`--height` change it. **Cmd+Q** on
macOS or **Ctrl+Q** elsewhere quits.

![Board picker](images/picker.png)

## Startup: the board picker

The picker is a scrollable list loaded from the embedded
`cmd/simui/boards.yaml`. Each card shows the board name, description and device
badges. Selecting one attaches its devices to the emulated chip and builds the
matching tabs; boards without a device do not get that tab.

Override the list without rebuilding:

```sh
RP2350SIM_BOARDS=/path/to/boards.yaml go run .
```

Startup flags:

| flag | meaning |
|---|---|
| `--board ID` | skip the picker and select a board (see `--list-boards`) |
| `--list-boards` | print board IDs, names and badges, then exit |
| `--version` | print the version and exit |
| `--tab NAME` | initial tab: `board`, `lcd`, `eth`/`wifi`, `ws`, `sensors`, `scope`, `logic`, `otp`, `bus`, `console` |
| `--width`, `--height` | window size in points |
| `--run SECONDS` | advance virtual time at startup; a live window keeps running, `--png` snapshots stay frozen |
| `--paused` | load the firmware but do not execute until Run |
| `--png PATH` | render one settled frame and exit (headless) |
| `--flash PATH` | raw flash backing image; self-updates are written back here |
| `--touch x,y` | inject a held touch at native panel coordinates |
| `--touch-demo` | move a synthetic touch around the panel while the UI runs |
| `--scope-zoom N`, `--scope-range V` | initial scope time zoom / full-scale volts |
| `--logic-trigger KIND` | arm the logic analyzer at startup: `off`, `any`, `uart`, `i2c`, `spi` |
| `--logic-pause` | pause the machine when the startup logic trigger fires |

For headless use, `--run` executes synchronously before the frame is rendered
(the UI does not step in the background), and the system font scan is awaited
so labels are present.

## Toolbar and machine lifecycle

The toolbar shows the CPU/board identity, the firmware path, **Open…** (OS file
dialog), **Load**, **Run**/**Pause**, **Reset**, **Board…** and the scope
controls. The status line under it reports the image name, virtual time, PC and
SP, or `(flash)` after a self-update.

- **Load** restarts the selected image, useful after a rebuild.
- **Reset** restarts the current image, preserving a self-updated flash.
- **Board…** returns to the picker; choosing another board reloads the current
  firmware onto the new hardware.
- After an OTA reset the **Board** tab shows a banner naming the flash image,
  its boot number and entry point.

## `boards.yaml`

A board declares the devices it has. Every key is optional; the UI creates a
tab only when the device exists. A board may carry several devices on one SPI
controller as long as each uses a different chip select; duplicate I2C
addresses and duplicate `bus`/`cs` pairs are rejected when the board loads.

| key | fields | tab |
|---|---|---|
| `lcd` | `spi`, `cs`, `dc`, `rst`, `width`, `height`, `rotation`, `backlight` | LCD |
| `touch` | `bus`, `addr`, `int`, `rst`, `width`, `height`, `rotation` | LCD, Sensors |
| `imu` | `bus`, `addr`, `int`, `int2` | Sensors |
| `battery` | `pin` (26–29), `divider`, `volts` | Sensors |
| `sd` | `spi`, `cs`, `dir` (host directory) or `image` (raw FAT image) | Sensors |
| `ethernet` | `kind` (`w5500`/`w6300`), `spi`, `cs`; or `kind: ch9120` with `uart`, `cfg0`, `rst`, `tcpcs` | Ethernet |
| `wifi` | `uart`, `cfg0`, `rst`, `tcpcs` | Wi-Fi |
| `ws2812` | `pin` | LEDs |
| `scope` | list of GPIO pins | default scope channels |
| `analog` | list of `{pin, wave, hz, amplitude, offset}` | generated signal (Scope/ADC) |
| `logic` | `uart {bus,tx,rx,baud}`, `i2c {bus,sda,scl}`, `spi {bus,sck,mosi,miso,cs}` | Logic |
| `serial` | list of `{uart, listen}` | Console (UART TCP bridge) |
| `i2c_tcp` | `{bus, addrs, listen \| connect}` | Bus |
| `spi_tcp` | `{bus, cs, listen \| connect}` | Bus |

`listen` defaults to `127.0.0.1:0` (a free port). `connect: host:port` makes the
emulator dial an external device server instead of listening.

```yaml
boards:
  - id: pico2
    name: Raspberry Pi Pico 2
    description: RP2350A · USB CDC console · on-board LED on GP25
    scope: [0, 1]
    logic:
      uart: { bus: 0, tx: 0, rx: 1, baud: 115200 }

  - id: pico2-bus
    name: RP2350 + remote I2C/SPI
    description: I2C0 at 0x50 and SPI0 CS GP17 exposed as Bus Pirate TCP devices
    i2c_tcp:
      - bus: 0
        addrs: [0x50]
    spi_tcp:
      - bus: 0
        cs: 17

  - id: geek
    name: Waveshare RP2350-GEEK
    lcd: { spi: 1, cs: 9, dc: 8, rst: 12, width: 240, height: 135,
           backlight: 25 }
    sd: { spi: 0, cs: 23, dir: sdcards/geek }

  - id: pico2-analog
    name: RP2350 + analog + protocols
    scope: [26, 0, 1]
    analog:
      - { pin: 26, wave: sine, hz: 1000, amplitude: 1.65, offset: 1.65 }
    logic:
      uart: { tx: 0, rx: 1, baud: 115200 }
      i2c: { sda: 4, scl: 5 }
      spi: { sck: 18, mosi: 19, miso: 16, cs: 17 }
```

`--list-boards` prints the full list. `docs/peripherals.md` maps each board to
its headless flags and demo firmware.

## Tabs

### Board

The Pico 2 header diagram and the live GPIO table (function, direction, level).
Panels size themselves to the window. Click a GPIO chip in the scope to add it.

![Board tab](images/board.png)

### LCD

The ST7789 framebuffer, only for boards with `lcd`. On a touch board, clicking
or dragging the panel injects a finger at the corresponding native panel
coordinate; the UI maps the displayed (rotated) frame back through the LCD
`rotation`. Release injects a lift.

![LCD tab](images/lcd.png)

### Sensors

Boards with `touch`, `imu`, `battery` or `sd` get a Sensors tab showing the live
touch point, acceleration/rate, die temperature, battery voltage, PWM backlight
duty and card size. The card state is read-only.

![Sensors tab](images/sensors.png)

### Ethernet / Wi-Fi

The guest identity (IP, mask, gateway, MAC), one row per socket and a live log
for W5500, W6300, CH9120 or the virtual Wi-Fi transport. A listening socket
shows both the emulated device address and the **host address to connect to**
(for example `connect to 127.0.0.1:9002`), so the firmware can be exercised with
`nc` or a browser.

![Ethernet tab with a CH9120 client](images/ethernet.png)

![Wi-Fi tab with the virtual netdev](images/wifi.png)

### LEDs

The last WS2812B frame as large swatches with its RGB values.

![LEDs tab](images/ws2812.png)

### Scope

Up to eight channels, each with its own colour, 0–3.3 V, 1 MHz sampling and a
~16 ms window. Click header pins (GP0–GP15, GP26–GP29) to add/remove traces;
the legend shows the latest voltage. **Time Zoom −/+** changes the window from
1× to 64×; **Range −/+** selects 0.5, 1, 1.65, 2, 3.3 or 5 V full scale;
**Reset** restores 1×/3.3 V. `analog:` board entries generate waveforms (the
`pico2-analog` board puts a 1 kHz sine on GP26, which the ADC also reads).

![Scope tab](images/scope.png)

### Logic

Board `logic:` protocols are tapped from the hardware models (UART, I2C, SPI)
or decoded from pin traces as a fallback for PIO/bit-banged signals. UART
bytes, I2C address/data/ACK phases and SPI MOSI/MISO bytes are listed under
synthesised waveforms; USB CDC transfers are listed with direction.

**Trigger** waits for Off/Any/UART/I2C/SPI, captures the trace and decoded lists
on the next event; **Pause: on** also stops the machine. **Rearm** waits for
the next event after a frozen capture. **Save trace** writes
`logic-analyze-trace-<date>-<board>.txt` in the current directory with the raw
pin samples and decoded UART/I2C/SPI/USB sections.

![Logic tab](images/logic.png)

### OTP

The RP2350 OTP array: 64 pages of 64 rows, each page showing its programmed-row
count and effective secure/non-secure lock. Click a page to inspect its rows
(data, raw 24-bit value and ECC status). The panel follows the last row the
firmware programmed until you pick a page. Nothing is persistent: seed rows
with the runner's `-otp` flag or dump them with `-otp-dump`.

![OTP tab](images/otp.png)

### Bus

The I2C/SPI TCP device bridges. Select a listening bridge and **Connect
terminal** to act as the device: the panel shows the Bus Pirate request tokens
and sends the `ACK`/`NAK`/hex responses you type. Responses are queued, so you
can arm a three-byte SPI read before the firmware asks for it. Each bridge keeps
its own terminal; the I2C and SPI devices can both be connected.

![Bus tab](images/bus.png)

`task ui:bus` opens the `pico2-bus` board on this tab, paused. For the bundled
`busmaster`, connect I2C **and** SPI, queue `ACK ACK 0x12 0x34` and
`0xEF 0x40 0x18`, then press **Run**. The UI bridge deadline is 500 ms; a
missing answer returns NAK/`0xff` and the machine keeps running. Use **Reset
firmware** to restart after queueing when the firmware already ran.

Connect-mode bridges have no terminal: their external server is the device,
exactly like the CLI `=host:port` form. See
[peripherals.md](peripherals.md#bus-pirate-i2c-and-spi-targets) for the token
protocol.

### Console

USB CDC + UART output. Click it to give it keyboard focus; typed text and
Enter/Backspace/Tab/arrows are forwarded to the firmware. UART-over-TCP and
bus-bridge status lines are listed here.

![Console tab](images/console.png)

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

## Headless snapshots

```sh
cd cmd/simui
go run . --board geek --tab lcd --run 0.6 --png ../../ui.png ../../build/lcd.elf
go run . --board pico2-analog --tab scope --run 0.02 --png ../../scope.png
go run . --board pico2-analog --tab logic --run 0.05 --png ../../logic.png ../../build/protocols.elf
go run . --board touch-lcd2 --tab sensors --touch 120,200 --run 0.6 \
  --png ../../sensors.png ../../build/touchlcd.elf
go run . --board pico2 --flash /tmp/ota-flash.bin --run 0.5 --png ../../ota.png \
  ../../firmware/c/build/ota.elf
```

`task ui:ota` opens the same setup live; `task ui:ota-png` writes
`build/ui-ota.png`. The screenshots in this guide are produced the same way.

## Keyboard

- Console focus: click the console panel. Text and special keys are forwarded
  to the firmware's USB CDC and UART0.
- The path text field owns typing while focused; the console does not steal it.
- Tab moves focus between focusable widgets.
- **Cmd+Q** (macOS) / **Ctrl+Q** quits.

See [peripherals.md](peripherals.md) for every device's CLI/UI workflow and
[boards.md](boards.md) for wiring.
