# Device plugin examples

These files are loaded by `pico2-plugins` and by the task examples.

- `bme280.lua` — a small register-pointer I2C sensor with chip id `0x60`.
- `mpu6050.lua` — WHO_AM_I `0x68` and a register pointer.
- `ssd1306.lua` — an I2C command/data acceptor for the display-on probe.
- `w25q16.lua` — SPI NOR JEDEC ID (`EF 40 15`) and status probe.
- `uart-modem.lua` — an AT-style serial companion; `print`/`uart_send` show
  the request/response shape for an nRF5340-style mock.
- `env-sensor.lua` — a register-level I2C environment sensor with drifting
  temperature, humidity and pressure; used by the Touch-LCD-2 dashboard.
- `aq-sensor.lua` — a UART air-quality sensor speaking the same ASCII protocol
  as the compiled example; useful for swapping Lua/WASM without firmware edits.
- `aq-sensor.wasm` — the compiled UART air-quality model from
  `wasm/aq-sensor.c`, including `host_uart_send` and `host_log` imports.
- `spi-flash.wasm` — the compiled ABI fixture from `wasm/spi-flash.c`.

The Lua runtime is bundled at `internal/deviceplugin/assets/device_lua.wasm`.
See `docs/plugins.md` for the API, the compiled ABI, and the board/CLI schema.
