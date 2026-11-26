#!/bin/sh
# Build the bundled Lua device-plugin runtime.
#
#     LUA_SRC=/tmp/lua-5.4.7 scripts/plugins/build.sh
#
# The artifact is checked in at internal/deviceplugin/assets/device_lua.wasm:
# users ship .lua files, never a C toolchain. Lua is compiled as C++ with the
# wasm exception-handling proposal (Lua raises with longjmp, which wasm only
# provides through EH); see ../robotexplorer/scripts/interp/build.sh for the
# same recipe.
set -e
cd "$(dirname "$0")/../.."

WASI_SDK=${WASI_SDK:-$HOME/wasi-sdk}
LUA_VERSION=5.4.7
LUA_SRC=${LUA_SRC:-/tmp/lua-$LUA_VERSION}
OUT=internal/deviceplugin/assets/device_lua.wasm

[ -d "$LUA_SRC" ] || {
	echo "lua source not found at $LUA_SRC:"
	echo "  curl -L -o /tmp/lua.tar.gz https://www.lua.org/ftp/lua-$LUA_VERSION.tar.gz"
	echo "  tar xzf /tmp/lua.tar.gz -C /tmp"
	exit 1
}
[ -x "$WASI_SDK/bin/clang++" ] || {
	echo "wasi-sdk not found at $WASI_SDK (WASI_SDK=... to point at it)"
	exit 1
}

SR=$WASI_SDK/share/wasi-sysroot
EH=$SR/lib/wasm32-wasip1/eh
LS=$LUA_SRC/src
SRCS="$LS/lapi.c $LS/lcode.c $LS/lctype.c $LS/ldebug.c $LS/ldo.c $LS/ldump.c $LS/lfunc.c
      $LS/lgc.c $LS/llex.c $LS/lmem.c $LS/lobject.c $LS/lopcodes.c $LS/lparser.c
      $LS/lstate.c $LS/lstring.c $LS/ltable.c $LS/ltm.c $LS/lundump.c $LS/lvm.c $LS/lzio.c
      $LS/lauxlib.c $LS/lbaselib.c $LS/lcorolib.c $LS/ldblib.c $LS/lmathlib.c
      $LS/lstrlib.c $LS/ltablib.c $LS/lutf8lib.c"

mkdir -p "$(dirname "$OUT")"
echo "building device_lua.wasm (Lua $LUA_VERSION) with $WASI_SDK/bin/clang++"
$WASI_SDK/bin/clang++ --target=wasm32-wasip1 --sysroot="$SR" \
	-mexec-model=reactor -O2 -DNDEBUG \
	-fwasm-exceptions -mllvm -wasm-use-legacy-eh=false \
	-D_WASI_EMULATED_SIGNAL \
	-I"$LS" -Iscripts/plugins/lua \
	-x c++ $SRCS scripts/plugins/lua/shim.c \
	-L"$EH" -lc++ -lc++abi -lunwind -lwasi-emulated-signal -lm \
	-Wl,--no-entry -Wl,--export-memory -Wl,--strip-all \
	-Wl,--export=plugin_abi_version -Wl,--export=plugin_alloc \
	-Wl,--export=plugin_init -Wl,--export=plugin_reset \
	-Wl,--export=plugin_tick \
	-Wl,--export=i2c_begin -Wl,--export=i2c_write \
	-Wl,--export=i2c_read -Wl,--export=i2c_end \
	-Wl,--export=spi_select -Wl,--export=spi_transfer \
	-Wl,--export=uart_tx \
	-Wl,--initial-memory=2097152 -Wl,--max-memory=33554432 \
	-o "$OUT"
sha256sum "$OUT"
ls -l "$OUT"
