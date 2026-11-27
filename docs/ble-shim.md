# BLE controller shim — design spike (M0)

Status: **M1–M3 implemented, plus the M5 HCI log and BLE tab.** The virtual
controller, CYW43 symbol binding, temperature peer, CLI flow, pcapng capture
and `pico2-w-ble` UI board are in place. The Wi-Fi side has a real gSPI model
(`internal/devices/cyw43439`, see [boards.md](boards.md)), and its Bluetooth
shared-memory ring now carries HCI for the TinyGo `soypat/cyw43439` driver
(`firmware/pico2wble`). The stock Pico SDK BTstack firmware still uses these
symbol hooks because its own PIO/DMA gSPI driver is not modelled. M4
(pairing/bonding/multiple connections) and M6's RF/PHY realism remain open.

This page records the seam, the exact symbols to bind and the HCI subset the
controller implements.

## Scope

The host stack is real firmware: btstack's GAP/GATT/ATT/SM/L2CAP code runs
unmodified on the Cortex-M33. The emulator owns the controller below the HCI
boundary: advertising, scanning, initiating, connection events and supervision.

| Layer | Owner |
|---|---|
| App, GATT/ATT, SM, L2CAP | firmware (stock btstack) |
| HCI transport | firmware, plus the emulator's bus hooks |
| Link layer | `internal/devices/ble` (new) |
| PHY, 2.4 GHz timing, whitening, CRC, hopping | not modelled |

**Non-goals for v1:** BR/EDR Classic, mesh, LE Audio/isochronous, 2M/Coded PHY
timing, Wi-Fi coexistence and RF capture. `pico2_w` still has no radio; the
existing virtual Wi-Fi is a CH9120 UART netdev, not a CYW43439.

## Seam decision

**Use symbol hooks at the CYW43 low-level bus boundary (option A), not at the
`cyw43_bluetooth_hci_*` wrappers.** A paravirtual MMIO transport (option B)
stays as the wasm/CI fallback. The Wi-Fi side has a real gSPI model
(`internal/devices/cyw43439`), and BLE is now carried over its shared-memory
ring for the TinyGo `soypat/cyw43439` driver. The symbol hooks remain for the
stock Pico SDK BTstack firmware because the C SDK's PIO/DMA gSPI driver is not
modelled.

The stock Pico W example calls `cyw43_arch_init()` before `btstack_main()`.
That path is:

```
cyw43_arch_init()
  -> cyw43_driver_init()          // state + HOST_WAKE IRQ, no chip I/O yet
  -> btstack_cyw43_init()         // btstack_run_loop_init + hci_init
hci_power_control(HCI_POWER_ON)
  -> hci_transport_cyw43_open()
     -> cyw43_bluetooth_hci_init()
        -> cyw43_ensure_bt_up()
           -> cyw43_ensure_up()   // WL_REG_ON, 20+50 ms, cyw43_ll_bus_init
           -> cyw43_btbus_init()  // BT firmware download
```

`cyw43_driver_init()` initialises `cyw43_state` and installs the HOST_WAKE
falling-edge IRQ, but does not touch the bus. The first bus traffic is
`cyw43_ensure_up()`, reached from the HCI transport open. After the bus-init
hook, `CYW43_USE_OTP_MAC=1` makes `cyw43_ensure_up()` call
`cyw43_ll_wifi_get_mac()`, so that symbol is part of the hook set as well.

Hooking the three public `cyw43_bluetooth_hci_*` wrappers would leave
`cyw43_state.bt_loaded` false; `cyw43_poll_func()` only calls
`cyw43_bluetooth_hci_process()` when `bt_loaded` is set. Bypassing the wrappers
therefore breaks the poll path. Hooking the lower bus functions keeps that
state machine real and gives the emulator the same 4-byte CYW43 header the
stock driver uses.

## Hookable symbols

Verified against pico-sdk 2.3.1 (`/usr/share/pico-sdk`) and its bundled
btstack 1.6.2. All of these are non-static, cross-translation-unit symbols, so
they survive `-O2` but not LTO.

| Symbol | Signature | Emulator behaviour |
|---|---|---|
| `cyw43_ll_bus_init` | `int(cyw43_ll_t *, const uint8_t *)` | return 0; skip chip reset/test register |
| `cyw43_ll_bus_sleep` | `void(cyw43_ll_t *, bool)` | no-op |
| `cyw43_ll_wifi_get_mac` | `int(cyw43_ll_t *, uint8_t *)` | copy the board MAC; `CYW43_USE_OTP_MAC=1` otherwise reads the backplane |
| `cyw43_btbus_init` | `int(cyw43_ll_t *)` | return 0; no BT firmware download |
| `cyw43_btbus_write` | `int(uint8_t *, uint32_t)` | parse HCI at `buf+4`, type at `buf[3]` |
| `cyw43_btbus_read` | `int(uint8_t *, uint32_t, uint32_t *)` | fill header + HCI event, set `*size` |
| `cyw43_ll_bt_has_work` | `bool(cyw43_ll_t *)` | true when an HCI event is queued |
| `cyw43_ll_has_work` | `bool(cyw43_ll_t *)` | false; no Wi-Fi backplane traffic |
| `cyw43_gpio_set` | `int(cyw43_t *, int, bool)` | record the LED, no bus access |

Sources:

- `lib/cyw43-driver/src/cyw43_ctrl.c` — `cyw43_init`, `cyw43_ensure_up`,
  `cyw43_poll_func`, `cyw43_bluetooth_hci_*`, `cyw43_gpio_set`.
- `lib/cyw43-driver/src/cyw43_ll.c` — `cyw43_ll_bus_init`,
  `cyw43_ll_bus_sleep`, `cyw43_ll_has_work`, `cyw43_ll_bt_has_work`.
- `lib/cyw43-driver/src/cyw43_btbus.h` — `cyw43_btbus_init/read/write`.
- `src/rp2_common/pico_cyw43_driver/btstack_hci_transport_cyw43.c` — the
  transport that prepends the 4-byte header (`buf[3]` is the HCI packet type).
- `src/rp2_common/pico_cyw43_driver/btstack_chipset_cyw43.c` — the only vendor
  command, `0xFC06` set BD_ADDR.

Wire format at this seam:

```
write: buf[0..2] CYW43 header, buf[3] HCI packet type, buf[4..] HCI packet
read:  buf[3] HCI packet type, buf[4..] HCI event; *size includes 4
```

## Symbol-hook binding

The existing `romStub`/`addHook` machinery only dispatches synthetic bootrom
addresses (`internal/rp2350/bootrom.go`), so it is not reused directly. A new,
small hook registry in `internal/rp2350` is enough:

```go
// InstallSymbolHook traps the function entry at addr. fn reads arguments from
// R0-R3 and returns with R0 set; the caller's LR is restored as the PC.
func (c *Chip) InstallSymbolHook(addr uint32, fn func(*m33.CPU)) bool
```

`Chip.stepCore` already checks the bootrom before fetching. Add the symbol
check next to it, before the instruction fetch, and call the handler with the
CPU at the function entry. The handler sets `R0` and `PC = LR &^ 1`; it must
not touch `R4-R11`. All hookable functions here take at most three register
arguments, so no stack-argument handling is needed.

Binding happens after the image is loaded, where both front ends already have
`*debug.Info`:

```go
for _, name := range []string{
    "cyw43_ll_bus_init", "cyw43_ll_bus_sleep", "cyw43_btbus_init",
    "cyw43_btbus_write", "cyw43_btbus_read", "cyw43_ll_bt_has_work",
    "cyw43_ll_has_work", "cyw43_gpio_set",
} {
    addr, ok := info.Resolve(name)
    if !ok {
        return fmt.Errorf("ble: %s missing from the ELF (LTO or a different SDK?)", name)
    }
    chip.InstallSymbolHook(addr, hooks[name])
}
```

Missing symbols must fail loudly. A silent fallback would either hang in the
real driver or advertise a controller that never responds. Version-gate the
binding on the SDK/btstack pair and log the resolved addresses.

## Event delivery

`cyw43_poll_func` runs from the HOST_WAKE falling-edge IRQ and calls
`cyw43_bluetooth_hci_process` only when `cyw43_ll_bt_has_work` is true. The BLE
model therefore drives the board's HOST_WAKE pin low when an HCI event is
queued, then releases it; the firmware's existing GPIO IRQ does the rest. No
firmware polling timer and no per-cycle hook are needed.

`cyw43_ll_has_work` is forced false so the same poll cannot enter the Wi-Fi
backplane path. `cyw43_ll_bt_has_work` is answered from the event queue.

BLE timers (advertising interval, connection interval, supervision timeout)
use `chip.AddDeadline`, never `AddPeriodicHook` for edge events. The controller
deadline is the earliest of those events; an idle fast-forward can therefore
land exactly on it.

## HCI subset for the first controller

M1 implements pure-Go codecs and table-driven tests first. The boot sequence
below is derived from btstack 1.6.2 `src/hci.c` (the `HCI_STATE_INITIALIZING`
path); M2 captures the exact sequence from a real ELF and records it here.

| Command | Opcode | First reply |
|---|---|---|
| HCI Reset | `0x0C03` | Command Complete, status 0 |
| Read Local Version Information | `0x1001` | HCI version, CYW43-style manufacturer |
| Read Local Supported Commands | `0x1002` | 64-byte bitmap |
| Read Buffer Size | `0x1005` | ACL/SCO packet lengths |
| Read BD_ADDR | `0x1009` | board address |
| LE Read Buffer Size | `0x2002` | LE ACL length/count |
| LE Read Local Supported Features | `0x2003` | LE feature bitmap |
| LE Set Event Mask | `0x2001` | status 0 |
| Set Event Mask | `0x0C01` | status 0 |
| Write LE Host Supported | `0x0C6D` | status 0 |
| vendor Set BD_ADDR | `0xFC06` | status 0 |

Feature-gated commands (LE Read Buffer Size v2, LE Read Maximum Data Length,
LE Read White List Size, LE Set Host Feature and the extended advertising
commands) must be answered according to the advertised feature bitmap. A
command the controller does not implement must return a Command Status error
with the opcode in the diagnostic, never a fabricated Command Complete.

GAP flow for M3: `LE Set Advertising Parameters` (`0x2006`), `LE Set
Advertising Data` (`0x2008`), `LE Set Advertising Enable` (`0x200A`), and on
the peer side `LE Set Scan Parameters` (`0x200B`), `LE Set Scan Enable`
(`0x200C`), `LE Create Connection` (`0x200D`), `LE Connection Update`
(`0x2013`) and `Disconnect` (`0x0406`).

## Prerequisites checked

- **TRNG:** implemented (`internal/rp2350/trng.go`), but the controller seeds
  its own deterministic PRNG from the board seed. Never use the global
  `math/rand`.
- **SHA-256:** implemented (`internal/rp2350/trng.go`); btstack's software
  AES/ECDH is the normal path, so no crypto accelerator is required.
- **Deadline scheduler:** `Chip.AddDeadline` exists and is the right primitive.
- **Pins:** the CYW43 pins are board wiring, not chip registers. Register
  WL_REG_ON, WL_CS, WL_CLK, WL_DATA and WL_HOST_WAKE through the UI
  `addDevicePin` path (and the CLI board flags) so LCD/Ethernet/BLE collisions
  fail at board load.
- **Wi-Fi naming:** `WifiDevice` in `cmd/simui/boards.go` is a CH9120 UART
  netdev with `e.ethKind = "wifi"`. Rename it (`UARTNetdevDevice`,
  `uart-netdev`) before adding BLE so `pico2_w` can carry both without
  misreporting either.

## Milestones

| Step | Deliverable | Exit criterion |
|---|---|---|
| M0 | this document | hook table and seam agreed |
| M1 | `internal/devices/ble` codec + controller skeleton | table-driven boot tests |
| M2 | symbol-hook binding, btbus transport | unmodified Pico W btstack ELF reaches `HCI_STATE_WORKING`; captured command/event log |
| M3 | local medium + heart-rate peer | scan, connect, MTU, discover, notify; `-expect` passes |
| M4 | multiple connections, SM, bonding | btstack security/battery examples pass |
| M5 | BLE tab + HCI log | decoded ATT tree, cycle-stamped events |
| M6 | two-chip CI scenario | bit-identical run, JUnit output |

M0 cannot honestly produce a boot-HCI log without M2's binding. The table
above is source-derived; M2 replaces it with the captured sequence.

## Risks

- **LTO or a different SDK inlines the symbols.** Detect at bind time and fail
  with the missing symbol names; fall back to the paravirtual MMIO transport.
- **`cyw43_arch_init` grows a bus access.** The hook set skips the known paths;
  any new one must fail loudly, not hang.
- **The controller silently accepts an unknown HCI command.** Return an error
  status and log the opcode.
- **Scope creep into gSPI.** The Wi-Fi gSPI model is separate; BLE over that
  transport is deferred. The bthci UART path (`cyw43_bthci_uart.c`) is a
  different transport and is not used by the Pico W.
