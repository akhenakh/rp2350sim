/* Device-plugin Lua shim.
 *
 * This is the Lua artifact's side of the device plugin ABI. The script is
 * loaded from the host, then its `new(config)` constructor returns a table
 * whose colon methods implement the bus callbacks:
 *
 *   i2c_begin(addr) / i2c_write(b)->ack / i2c_read()->byte,ack / i2c_end()
 *   spi_select(active) / spi_transfer(tx)->byte
 *   uart_tx(b) / tick(us) / reset()
 *
 * The host never allocates inside this module: the script arrives through
 * plugin_rt.program_read and the config through the init buffer.
 *
 * Build with scripts/plugins/build.sh (wasi-sdk, C++ exceptions for Lua).
 */
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "lua.h"
#include "lauxlib.h"
#include "lualib.h"

/* --- host imports -------------------------------------------------------- */
extern "C" {
#define PLUGIN_IMPORT(name, ret, ...) \
    __attribute__((import_module("plugin"), import_name(#name))) extern ret name(__VA_ARGS__)

PLUGIN_IMPORT(host_log, void, const char *, int32_t);
PLUGIN_IMPORT(host_now_us, int64_t, void);
PLUGIN_IMPORT(host_gpio_write, int32_t, int32_t, int32_t);
PLUGIN_IMPORT(host_gpio_read, int32_t, int32_t);
PLUGIN_IMPORT(host_uart_send, int32_t, const char *, int32_t);

__attribute__((import_module("plugin_rt"), import_name("program_len")))
extern int32_t program_len(void);
__attribute__((import_module("plugin_rt"), import_name("program_read")))
extern int32_t program_read(char *buf, int32_t cap);
}

/* Lua's table library seeds its RNG from clock(); use the simulator's clock
 * rather than a wall clock so a plugin run is repeatable. */
extern "C" clock_t clock(void) {
    return (clock_t)host_now_us();
}

/* Lua's math.randomseed uses time(NULL); use virtual microseconds so two
 * runs of the same plugin see the same seed. */
extern "C" time_t time(time_t *out) {
    time_t v = (time_t)(host_now_us() / 1000000);
    if (out != nullptr) {
        *out = v;
    }
    return v;
}

static lua_State *L;
static int instance_ref = LUA_NOREF;
static int failed;

static void say(const char *s, int32_t n) {
    if (s != nullptr && n > 0) {
        host_log(s, n);
    }
}

static void report(lua_State *l, const char *where) {
    const char *msg = lua_tostring(l, -1);
    if (msg == nullptr) {
        msg = "error object is not a string";
    }
    luaL_traceback(l, l, msg, 1);
    const char *trace = lua_tostring(l, -1);
    if (trace != nullptr) {
        say(trace, (int32_t)strlen(trace));
    }
    lua_pop(l, 2);
    failed = 1;
}

static int l_print(lua_State *l) {
    /* luaL_buffinit may push its buffer box onto the stack; capture the
     * argument count first or the box is printed as an extra value. */
    int n = lua_gettop(l);
    luaL_Buffer b;
    luaL_buffinit(l, &b);
    for (int i = 1; i <= n; i++) {
        size_t len = 0;
        const char *s = luaL_tolstring(l, i, &len);
        if (i > 1) {
            luaL_addchar(&b, ' ');
        }
        luaL_addlstring(&b, s, len);
        lua_pop(l, 1);
    }
    luaL_pushresult(&b);
    size_t len = 0;
    const char *s = lua_tolstring(l, -1, &len);
    say(s, (int32_t)len);
    lua_pop(l, 1);
    return 0;
}

static int push_config(const char *ptr, int32_t len) {
    if (len <= 0) {
        lua_newtable(L);
        return 1;
    }
    char *buf = (char *)malloc((size_t)len + 8);
    if (buf == nullptr) {
        return 0;
    }
    memcpy(buf, "return ", 7);
    memcpy(buf + 7, ptr, (size_t)len);
    int status = luaL_loadbuffer(L, buf, (size_t)len + 7, "=plugin-config");
    free(buf);
    if (status != LUA_OK || lua_pcall(L, 0, 1, 0) != LUA_OK) {
        report(L, "config");
        return 0;
    }
    if (!lua_istable(L, -1)) {
        lua_pop(L, 1);
        say("plugin config is not a table", 28);
        return 0;
    }
    return 1;
}

static int push_instance(void) {
    if (instance_ref == LUA_NOREF) {
        return 0;
    }
    lua_rawgeti(L, LUA_REGISTRYINDEX, instance_ref);
    return 1;
}

static int lua_pcall_method(const char *name, int nargs, int nres) {
    if (!push_instance()) {
        return -1;
    }
    lua_getfield(L, -1, name);
    if (!lua_isfunction(L, -1)) {
        lua_pop(L, 2);
        return -1;
    }
    lua_pushvalue(L, -2);
    /* Arguments are already above self, and pcall accepts them in place. */
    if (lua_pcall(L, nargs + 1, nres, 0) != LUA_OK) {
        report(L, name);
        return -1;
    }
    return 0;
}

/* Convenience for scripts: send bytes back to the firmware UART. */
static int l_uart_send(lua_State *l) {
    size_t len = 0;
    const char *s = luaL_checklstring(l, 1, &len);
    host_uart_send(s, (int32_t)len);
    return 0;
}

static int l_gpio_write(lua_State *l) {
    int pin = (int)luaL_checkinteger(l, 1);
    int level = lua_toboolean(l, 2) ? 1 : 0;
    host_gpio_write(pin, level);
    return 0;
}

static int l_gpio_read(lua_State *l) {
    lua_pushinteger(l, host_gpio_read((int)luaL_checkinteger(l, 1)));
    return 1;
}

static int l_now_us(lua_State *l) {
    lua_pushinteger(l, (lua_Integer)host_now_us());
    return 1;
}

static int l_log(lua_State *l) {
    return l_print(l);
}

static void install_helpers(void) {
    lua_pushcfunction(L, l_uart_send);
    lua_setglobal(L, "uart_send");
    lua_pushcfunction(L, l_gpio_write);
    lua_setglobal(L, "gpio_write");
    lua_pushcfunction(L, l_gpio_read);
    lua_setglobal(L, "gpio_read");
    lua_pushcfunction(L, l_now_us);
    lua_setglobal(L, "now_us");
    lua_pushcfunction(L, l_log);
    lua_setglobal(L, "log");
}

extern "C" int32_t plugin_abi_version(void) { return 1; }

extern "C" void *plugin_alloc(int32_t n) {
    if (n <= 0) {
        return nullptr;
    }
    return malloc((size_t)n);
}

extern "C" int32_t plugin_init(const char *config, int32_t config_len) {
    if (L != nullptr) {
        return 0;
    }
    L = luaL_newstate();
    if (L == nullptr) {
        return 1;
    }
    luaL_requiref(L, "_G", luaopen_base, 1);
    lua_pop(L, 1);
    luaL_requiref(L, "table", luaopen_table, 1);
    lua_pop(L, 1);
    luaL_requiref(L, "string", luaopen_string, 1);
    lua_pop(L, 1);
    luaL_requiref(L, "math", luaopen_math, 1);
    lua_pop(L, 1);
    luaL_requiref(L, "utf8", luaopen_utf8, 1);
    lua_pop(L, 1);
    luaL_requiref(L, "coroutine", luaopen_coroutine, 1);
    lua_pop(L, 1);
    install_helpers();

    /* No filesystem entry points even if the base library exposes them. */
    lua_pushnil(L);
    lua_setglobal(L, "dofile");
    lua_pushnil(L);
    lua_setglobal(L, "loadfile");
    lua_pushcfunction(L, l_print);
    lua_setglobal(L, "print");

    int32_t len = program_len();
    if (len <= 0) {
        say("plugin has no program", 21);
        return 1;
    }
    char *src = (char *)malloc((size_t)len + 1);
    if (src == nullptr) {
        return 1;
    }
    int32_t got = program_read(src, len);
    src[got] = '\0';
    int status = luaL_loadbuffer(L, src, (size_t)got, "=plugin");
    free(src);
    if (status != LUA_OK) {
        report(L, "load");
        return 1;
    }
    if (lua_pcall(L, 0, 1, 0) != LUA_OK) {
        report(L, "run");
        return 1;
    }
    if (lua_isfunction(L, -1)) {
        if (lua_pcall(L, 0, 1, 0) != LUA_OK || !lua_istable(L, -1)) {
            report(L, "module");
            return 1;
        }
    }
    if (!lua_istable(L, -1)) {
        say("plugin must return a table", 27);
        return 1;
    }

    lua_getfield(L, -1, "new");
    if (lua_isfunction(L, -1)) {
        /* `new` is a plain function: new(config), not a colon method. The
         * returned table's bus methods are colon methods. */
        if (!push_config(config, config_len)) {
            lua_pop(L, 3);
            return 1;
        }
        if (lua_pcall(L, 1, 1, 0) != LUA_OK) {
            report(L, "new");
            return 1;
        }
        if (!lua_istable(L, -1)) {
            say("plugin constructor did not return a table", 42);
            return 1;
        }
        instance_ref = luaL_ref(L, LUA_REGISTRYINDEX);
        lua_pop(L, 1); /* module table */
    } else {
        lua_pop(L, 1); /* new */
        instance_ref = luaL_ref(L, LUA_REGISTRYINDEX);
    }
    return 0;
}

extern "C" void plugin_reset(void) {
    if (failed || L == nullptr || instance_ref == LUA_NOREF) {
        return;
    }
    if (lua_pcall_method("reset", 0, 0) == 0) {
        lua_pop(L, 1); /* instance */
    }
}

extern "C" void plugin_tick(int64_t us) {
    if (failed || L == nullptr || instance_ref == LUA_NOREF) {
        return;
    }
    lua_rawgeti(L, LUA_REGISTRYINDEX, instance_ref);
    lua_getfield(L, -1, "tick");
    if (!lua_isfunction(L, -1)) {
        lua_pop(L, 2);
        return;
    }
    lua_pushvalue(L, -2);
    lua_pushinteger(L, (lua_Integer)us);
    if (lua_pcall(L, 2, 0, 0) != LUA_OK) {
        report(L, "tick");
        return;
    }
    lua_pop(L, 1); /* instance */
}

extern "C" void i2c_begin(int32_t addr) {
    if (failed || L == nullptr || instance_ref == LUA_NOREF) {
        return;
    }
    lua_rawgeti(L, LUA_REGISTRYINDEX, instance_ref);
    lua_getfield(L, -1, "i2c_begin");
    if (!lua_isfunction(L, -1)) {
        lua_pop(L, 2);
        return;
    }
    lua_pushvalue(L, -2);
    lua_pushinteger(L, addr);
    if (lua_pcall(L, 2, 0, 0) != LUA_OK) {
        report(L, "i2c_begin");
        return;
    }
    lua_pop(L, 1);
}

extern "C" int32_t i2c_write(int32_t b) {
    if (failed || L == nullptr || instance_ref == LUA_NOREF) {
        return 0;
    }
    lua_rawgeti(L, LUA_REGISTRYINDEX, instance_ref);
    lua_getfield(L, -1, "i2c_write");
    if (!lua_isfunction(L, -1)) {
        lua_pop(L, 2);
        return 0;
    }
    lua_pushvalue(L, -2);
    lua_pushinteger(L, b);
    if (lua_pcall(L, 2, 1, 0) != LUA_OK) {
        report(L, "i2c_write");
        return 0;
    }
    int ack = lua_toboolean(L, -1);
    lua_pop(L, 2);
    return ack ? 1 : 0;
}

extern "C" int32_t i2c_read(void) {
    if (failed || L == nullptr || instance_ref == LUA_NOREF) {
        return -1;
    }
    lua_rawgeti(L, LUA_REGISTRYINDEX, instance_ref);
    lua_getfield(L, -1, "i2c_read");
    if (!lua_isfunction(L, -1)) {
        lua_pop(L, 2);
        return -1;
    }
    lua_pushvalue(L, -2);
    if (lua_pcall(L, 1, 2, 0) != LUA_OK) {
        report(L, "i2c_read");
        return -1;
    }
    int32_t value = (int32_t)lua_tointeger(L, -2);
    int ack = lua_toboolean(L, -1);
    lua_pop(L, 3);
    if (value < 0 || value > 255 || !ack) {
        return -1;
    }
    return value;
}

extern "C" void i2c_end(void) {
    if (failed || L == nullptr || instance_ref == LUA_NOREF) {
        return;
    }
    lua_rawgeti(L, LUA_REGISTRYINDEX, instance_ref);
    lua_getfield(L, -1, "i2c_end");
    if (!lua_isfunction(L, -1)) {
        lua_pop(L, 2);
        return;
    }
    lua_pushvalue(L, -2);
    if (lua_pcall(L, 1, 0, 0) != LUA_OK) {
        report(L, "i2c_end");
        return;
    }
    lua_pop(L, 1);
}

extern "C" void spi_select(int32_t active) {
    if (failed || L == nullptr || instance_ref == LUA_NOREF) {
        return;
    }
    lua_rawgeti(L, LUA_REGISTRYINDEX, instance_ref);
    lua_getfield(L, -1, "spi_select");
    if (!lua_isfunction(L, -1)) {
        lua_pop(L, 2);
        return;
    }
    lua_pushvalue(L, -2);
    lua_pushboolean(L, active != 0);
    if (lua_pcall(L, 2, 0, 0) != LUA_OK) {
        report(L, "spi_select");
        return;
    }
    lua_pop(L, 1);
}

extern "C" int32_t spi_transfer(int32_t tx) {
    if (failed || L == nullptr || instance_ref == LUA_NOREF) {
        return 0xFF;
    }
    lua_rawgeti(L, LUA_REGISTRYINDEX, instance_ref);
    lua_getfield(L, -1, "spi_transfer");
    if (!lua_isfunction(L, -1)) {
        lua_pop(L, 2);
        return 0xFF;
    }
    lua_pushvalue(L, -2);
    lua_pushinteger(L, tx);
    if (lua_pcall(L, 2, 1, 0) != LUA_OK) {
        report(L, "spi_transfer");
        return 0xFF;
    }
    int32_t value = (int32_t)lua_tointeger(L, -1);
    lua_pop(L, 2);
    return value & 0xFF;
}

extern "C" void uart_tx(int32_t b) {
    if (failed || L == nullptr || instance_ref == LUA_NOREF) {
        return;
    }
    lua_rawgeti(L, LUA_REGISTRYINDEX, instance_ref);
    lua_getfield(L, -1, "uart_tx");
    if (!lua_isfunction(L, -1)) {
        lua_pop(L, 2);
        return;
    }
    lua_pushvalue(L, -2);
    lua_pushinteger(L, b);
    if (lua_pcall(L, 2, 0, 0) != LUA_OK) {
        report(L, "uart_tx");
        return;
    }
    lua_pop(L, 1);
}

