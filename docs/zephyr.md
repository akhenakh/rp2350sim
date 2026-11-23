# Zephyr

Zephyr v4.5.0 boots and runs on the emulated RP2350A Cortex-M33. Validated
targets and tests:

| Build | Result |
|---|---|
| `samples/hello_world` | boots and prints `Hello World! rpi_pico2/rp2350a/m33_0` |
| `samples/synchronization` | two preemptible threads print alternately |
| `samples/philosophers` | six threads, mutexes, timeslicing |
| `tests/kernel/context` | all suites pass |
| `tests/kernel/sched/schedule_api` | all suites pass |
| `tests/kernel/fpu_sharing/generic` | `test_load_store` and `test_pi` pass (lazy FP + context switch) |
| `samples/basic/blinky` | GP25 toggles |

The board target is the HWMv2 ARM cluster, `rpi_pico2/rp2350a/m33`. Zephyr
places its vector table at `0x10000000`, so plain `-fw` ELF loading works. The
default configuration is supervisor-only (`CONFIG_FPU` off, no MPU/userspace).

## Install (Arch / CachyOS)

The Zephyr SDK comes from the AUR. The ARM-only split package is enough for the
RP2350's Cortex-M33:

```sh
yay -S zephyr-sdk-gnu-arm-zephyr-eabi-bin ninja
```

Python tooling belongs in a venv; Arch's Python is externally managed and
Zephyr's `requirements-base.txt` needs packages that are not all packaged:

```sh
python -m venv ~/zephyr-venv
~/zephyr-venv/bin/pip install west
mkdir -p ~/zephyrproject && cd ~/zephyrproject
~/zephyr-venv/bin/west init -m https://github.com/zephyrproject-rtos/zephyr --mr v4.5.0 .
~/zephyr-venv/bin/west update
~/zephyr-venv/bin/pip install -r zephyr/scripts/requirements-base.txt
```

## Build and run

```sh
cd ~/zephyrproject
~/zephyr-venv/bin/west build -p always -b rpi_pico2/rp2350a/m33 \
  zephyr/samples/hello_world
./build/rp2350sim -fw ~/zephyrproject/build/zephyr/zephyr.elf -seconds 2 \
  -expect "Hello World! rpi_pico2" -stop-on-expect
```

`task zephyr:smoke` runs the validated set from `tools/zephyr-smoke.sh`; it
expects the workspace at `$ZEPHYR_WORKSPACE` (default `~/zephyrproject`), the
venv at `$ZEPHYR_VENV` (default `~/zephyr-venv`) and the SDK at
`$ZEPHYR_SDK_INSTALL_DIR` (default `/opt/zephyr-sdk`). It is not part of
`task ci` because it needs a full west workspace.

## Limits

- Supervisor configurations only: the MPU/SAU registers are stored but not
  enforced, so `CONFIG_USERSPACE` isolation and MPU stack guards do not work.
- Usage/bus/mem fault escalation is not generated; tests that deliberately
  fault (for example `tests/arch/arm/arm_interrupt`'s ESF test) halt instead.
- MSPLIM/PSPLIM are stored but violations are not checked.
