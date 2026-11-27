/* Air-quality sensor mock as a compiled WASM device plugin.
 *
 * The RP2350 sends ASCII commands on UART1: 'I' asks for the identity and 'R'
 * for a reading. The plugin answers with the same protocol as aq-sensor.lua,
 * so boards.yaml can swap the Lua and WASM models without touching firmware.
 *
 * ABI: plugin_abi_version/alloc/init/reset/tick and uart_tx, plus the
 * plugin.host_uart_send host import. Build with:
 *   clang --target=wasm32 -nostdlib -O2 -fno-builtin -Wall -Wextra \
 *     -Wl,--no-entry -Wl,--strip-all \
 *     -o plugins/examples/aq-sensor.wasm plugins/examples/wasm/aq-sensor.c
 */
#include <stdint.h>

#define IMPORT(name) \
    __attribute__((import_module("plugin"), import_name(name)))
#define EXPORT(name) __attribute__((export_name(name)))

IMPORT("host_log") void host_log(const char *p, uint32_t n);
IMPORT("host_uart_send") int32_t host_uart_send(const char *p, uint32_t n);

static int ticks;
static int phase;
static int pm = 14;
static int co2 = 640;
static int temp = 2230;
static int hum = 4800;

static int put(char *b, int n, char c) {
    b[n] = c;
    return n + 1;
}

static int put_str(char *b, int n, const char *s) {
    while (*s) {
        b[n++] = *s++;
    }
    return n;
}

static int put_uint(char *b, int n, int v) {
    char tmp[12];
    int k = 0;
    if (v == 0) {
        return put(b, n, '0');
    }
    while (v > 0 && k < (int)sizeof(tmp)) {
        tmp[k++] = (char)('0' + v % 10);
        v /= 10;
    }
    while (k > 0) {
        b[n++] = tmp[--k];
    }
    return n;
}

static void send_reading(void) {
    char buf[64];
    int n = 0;
    n = put_str(buf, n, "AQ,");
    n = put_uint(buf, n, pm);
    n = put(buf, n, ',');
    n = put_uint(buf, n, co2);
    n = put(buf, n, ',');
    n = put_uint(buf, n, temp);
    n = put(buf, n, ',');
    n = put_uint(buf, n, hum);
    n = put_str(buf, n, "\r\n");
    host_uart_send(buf, (uint32_t)n);
}

EXPORT("plugin_abi_version") int plugin_abi_version(void) { return 1; }
static unsigned char plugin_config[4096];
EXPORT("plugin_alloc") void *plugin_alloc(int n) {
    if (n < 0 || n > (int)sizeof(plugin_config)) return 0;
    return plugin_config;
}
EXPORT("plugin_init") int plugin_init(const char *config, int len) {
    (void)config;
    (void)len;
    host_log("aq-sensor wasm ready", 20);
    return 0;
}
EXPORT("plugin_reset") void plugin_reset(void) {
    ticks = 0;
    phase = 0;
    pm = 14;
    co2 = 640;
    temp = 2230;
    hum = 4800;
}

EXPORT("plugin_tick") void plugin_tick(int64_t us) {
    (void)us;
    ticks++;
    if (ticks % 250 != 0) {
        return;
    }
    phase = (phase + 1) % 40;
    int wave = phase < 20 ? phase : 40 - phase;
    pm = 12 + wave;
    co2 = 620 + phase * 9;
    temp = 2210 + phase * 5;
    hum = 4700 + phase * 14;
}

EXPORT("uart_tx") void uart_tx(int b) {
    if (b == 'I') {
        static const char id[] = "AQ-SENSOR 1.0 (wasm)\r\n";
        host_uart_send(id, (uint32_t)(sizeof(id) - 1));
    } else if (b == 'R') {
        send_reading();
    }
}
