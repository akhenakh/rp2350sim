# Bundled device-plugin runtime

`device_lua.wasm` is Lua 5.4.7 compiled as a wasm32-wasip1 reactor with
`wasi-sdk`, plus `scripts/plugins/lua/shim.c`. Lua is compiled as C++ with the
wasm exception-handling proposal because its error mechanism needs
`throw`/`catch`; see `scripts/plugins/build.sh`.

The artifact is checked in so a user can ship a `.lua` device file without a C
toolchain. Rebuild it with:

```sh
LUA_SRC=/tmp/lua-5.4.7 scripts/plugins/build.sh
```

Provenance:

- Lua 5.4.7 (MIT), <https://www.lua.org/>
- wasi-sdk 34 (`~/wasi-sdk`), wasm32-wasip1
- compiler flags: `-fwasm-exceptions -mllvm -wasm-use-legacy-eh=false`,
  reactor, stripped, 2 MiB initial / 32 MiB maximum memory
- SHA-256: see `sha256sum internal/deviceplugin/assets/device_lua.wasm`
