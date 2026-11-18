// pico_multicore: core 1 signals core 0 through the SIO FIFO.
#include <stdio.h>
#include "pico/stdlib.h"
#include "pico/multicore.h"

static volatile uint32_t worker_ok;

static void core1_entry(void) {
    multicore_fifo_push_blocking(0x5eed);
    while (true) {
        tight_loop_contents();
    }
}

int main(void) {
    stdio_init_all();
    multicore_launch_core1(core1_entry);
    uint32_t v = multicore_fifo_pop_blocking();
    worker_ok = (v == 0x5eed);
    printf("c multicore worker=%d\n", worker_ok);
    while (true) {
        tight_loop_contents();
    }
}
