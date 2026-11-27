# Bluetooth LE

`rp2350sim` can run stock **Pico W BTstack firmware** against a virtual BLE
controller. The host stack is real: btstack's GAP, GATT, ATT, SM and L2CAP code
runs unmodified on the emulated Cortex-M33. The emulator answers below the
CYW43 HCI transport, so there is no 2.4 GHz PHY.

This is a logical controller, not a packet-level radio: advertising, scanning,
connections and the scripted GATT peer are deterministic and driven by virtual
time.

## Transports

- **Symbol hooks (`-ble`)**: the stock Pico SDK BTstack firmware keeps its own
  `cyw43_btbus` driver; the emulator replaces `cyw43_ll_bus_init`,
  `cyw43_btbus_read/write` and the has-work checks. This is the path the
  `pico2-w-ble` board and `-ble` use.
- **gSPI shared bus (`-cyw43439`)**: the TinyGo `soypat/cyw43439` driver
  performs the real BT firmware download and HCI exchange through the chip's
  shared-memory rings. `firmware/pico2wble` validates an HCI Reset and
  `Read BD_ADDR` over that transport. The C SDK's own PIO/DMA gSPI driver is
  not modelled, so the stock BTstack fixture still uses the symbol hooks.


## What works

- HCI initialisation, controller reset, buffer sizes and LE feature exchange.
- LE advertising reports, scanning, initiating, connection complete,
  disconnect and connection parameter update.
- ATT/GATT: MTU exchange, service discovery, characteristic discovery, reads,
  CCCD writes and handle-value notifications.
- HCI traffic export as a pcapng file that Wireshark dissects as Bluetooth HCI
  H4 (`LINKTYPE_BLUETOOTH_HCI_H4`, 187).
- One scripted peer: Environmental Sensing service (`0x181A`) with a
  Temperature characteristic (`0x2A6E`), address `C0:DE:12:34:56:78`,
  reporting `23.45 °C`.

Not modelled: Classic/BR-EDR, pairing and bonding, LESC, 2M/Coded PHY,
extended advertising, multiple simultaneous connections, mesh, LE Audio and
Wi-Fi coexistence. The Pico 2 W's Wi-Fi side is the CYW43439 gSPI model
(`-cyw43439`); the legacy virtual Wi-Fi board remains a CH9120 UART netdev.

## Run the stock Pico W example

The fixture is the Pico W `ble_temp_reader` BTstack example, with stdio on
UART0. Build it with the Pico SDK (the SDK checkout must include the btstack
submodule):

```sh
task firmware:ble
./build/rp2350sim -fw build/ble-c/ble_temp_reader.elf -ble -seconds 3
```

Expected output:

```
Connecting to device with addr C0:DE:12:34:56:78.
Read temp 23.45 degc
Read temp 23.45 degc
...
```

The client scans for the Environmental Sensing service, connects, discovers
the temperature characteristic, enables notifications and prints each value.
The firmware is an unmodified Pico SDK/BTstack build; no source shim is needed.

| flag | meaning |
|---|---|
| `-ble` | attach the virtual controller and bind the CYW43 HCI symbols |
| `-ble-addr ADDR` | advertised peer address (default `C0:DE:12:34:56:78`) |
| `-ble-host-wake GPIO` | CYW43 HOST_WAKE pin (default 24, Pico W wiring) |
| `-ble-seed N` | controller PRNG seed for deterministic runs |
| `-ble-hci-log PATH` | write HCI commands, events and ACL data as pcapng |

`-ble` requires an ELF with a symbol table: the implementation binds
`cyw43_ll_bus_init`, `cyw43_ll_bus_sleep`, `cyw43_ll_wifi_get_mac`,
`cyw43_btbus_init/read/write` and the two has-work checks by address. A
stripped or LTO-inlined image fails at startup with the missing symbol name
instead of hanging in the real bus driver.

## Wireshark

```sh
./build/rp2350sim -fw build/ble-c/ble_temp_reader.elf -ble \
  -seconds 2 -quiet -ble-hci-log ble.pcapng
tshark -r ble.pcapng -Y btatt
```

The capture uses virtual microseconds, so replaying the same firmware produces
the same capture. The ATT reads, CCCD write and notifications are visible in
the `btatt` and `btgatt` dissectors.

## How it is wired

```
firmware (stock btstack)
  HCI transport (cyw43 HCI)
    cyw43_ll_bus_init / cyw43_btbus_read/write   <- symbol hooks
      internal/devices/ble.Controller             <- HCI codec, link layer
        internal/devices/ble.Peer                 <- scripted ATT/GATT server
```

The controller drives the board's HOST_WAKE pin low while an HCI event is
queued. The firmware's existing falling-edge IRQ runs `cyw43_poll_func`, the
transport reads the event and btstack processes it. Controller timers use
`Chip.AddDeadline`, never a per-cycle hook, so an idle board can still
fast-forward between connection events.

See [ble-shim.md](ble-shim.md) for the seam analysis and the exact hook set.
