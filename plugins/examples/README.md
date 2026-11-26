# Device plugin examples

These files are loaded by `pico2-plugins` and by the task examples.

- `bme280.lua` — a small register-pointer I2C sensor with chip id `0x60`.
- `w25q16.lua` — SPI NOR JEDEC ID (`EF 40 15`) and status probe.
- `uart-modem.lua` — an AT-style serial companion; `print`/`uart_send` show
  the request/response shape for an nRF5340-style mock.
- `spi-flash.wasm` — the compiled ABI fixture from `wasm/spi-flash.c`.

The Lua runtime is bundled at `internal/deviceplugin/assets/device_lua.wasm`.
See `docs/plugins.md` for the API, the compiled ABI, and the board/CLI schema.
