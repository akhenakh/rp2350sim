// debug holds known values for the debugger tests: globals, a function
// argument, and volatile locals that stay in memory at -O3.
#include <stdint.h>
#include "pico/stdlib.h"

volatile uint32_t g_magic = 0x12345678u;
volatile uint32_t g_wide_lo = 0x55667788u;
volatile uint32_t g_wide_hi = 0x11223344u;
volatile int32_t g_negative = -1234;

__attribute__((noinline)) void debug_marker(uint32_t arg) {
    volatile uint32_t local = arg ^ 0x0f0f0f0fu;
    volatile int32_t negative = -42;
    (void)local;
    (void)negative;
    while (true) {
        tight_loop_contents();
    }
}

int main(void) {
    stdio_init_all();
    g_magic = 0x12345678u;
    g_wide_lo = 0x55667788u;
    g_wide_hi = 0x11223344u;
    g_negative = -1234;
    if (g_magic == 0u || g_wide_lo == 0u || g_wide_hi == 0u || g_negative == 0) {
        return 1;
    }
    sleep_ms(100);
    debug_marker(0xa5a5a5a5u);
    return 0;
}
