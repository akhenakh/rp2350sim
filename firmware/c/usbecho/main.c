// Pico SDK USB CDC echo: the TinyUSB counterpart of the TinyGo echo example.
// It validates host-to-device CDC transfers (packet PIDs and the dynamically
// allocated endpoint buffers) on a descriptor-driven USB host.
#include <stdio.h>
#include "pico/stdlib.h"

int main(void) {
    stdio_init_all();
    // Wait for the host to configure the CDC interface so the banner is not
    // dropped by TinyUSB's pre-enumeration write gate.
    while (!stdio_usb_connected()) {
        sleep_ms(1);
    }
    printf("usb echo ready\n");
    fflush(stdout);
    while (true) {
        int c = getchar_timeout_us(0);
        if (c == PICO_ERROR_TIMEOUT) {
            tight_loop_contents();
            continue;
        }
        if (c == '\r') {
            putchar('\n');
        } else {
            printf("echo %c\n", c);
        }
        fflush(stdout);
    }
}
