# ble_temp_reader

Stock Pico W BTstack GATT client used by the virtual CYW43 BLE controller.

`client.c`, `btstack_config.h` and `btstack_config_common.h` are copied from the
pico-examples `bluetooth/ble_temp_sensor` example (SDK 2.3.1, commit
`0d62f75b`) under its BSD-3-Clause licence. The only change is UART stdio in
`CMakeLists.txt`, so the emulator's console captures the output.

```sh
task firmware:ble
task run:ble
```
