/* Minimal compiled SPI NOR plugin: JEDEC ID and status. It is a fixture for
 * the WASM half of the device-plugin ABI, not a full flash model. */
#include <stdint.h>

static int cmd;
static int phase;
static int out;
static int left;

int plugin_abi_version(void) { return 1; }
static unsigned char plugin_config[4096];
void *plugin_alloc(int n) {
    if (n < 0 || n > (int)sizeof(plugin_config)) return 0;
    return plugin_config;
}
int plugin_init(const char *config, int len) { (void)config; (void)len; return 0; }
void plugin_reset(void) {}
void plugin_tick(int64_t us) { (void)us; }

void i2c_begin(int addr) { (void)addr; }
int i2c_write(int b) { (void)b; return 0; }
int i2c_read(void) { return -1; }
void i2c_end(void) {}

void uart_tx(int b) { (void)b; }

void spi_select(int active) {
    if (active) {
        cmd = 0;
        phase = 0;
        out = 0;
        left = 0;
    }
}

int spi_transfer(int tx) {
    if (phase == 0) {
        cmd = tx;
        phase = 1;
        if (tx == 0x9F) { out = 0xEF; left = 2; return out; }
        if (tx == 0x05) { return 0; }
        return 0xFF;
    }
    if (left > 0) {
        left--;
        if (left == 1) return 0x40;
        return 0x15;
    }
    (void)cmd;
    return 0xFF;
}
