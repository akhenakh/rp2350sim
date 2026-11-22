// Stock Pico SDK hello world over USB CDC (TinyUSB), the pico_enable_stdio_usb
// counterpart of the UART example.
#include <stdio.h>
#include "pico/stdlib.h"

int main(void) {
    stdio_init_all();
    for (int i = 0; i < 3; i++) {
        printf("hello usb %d\n", i);
        sleep_ms(50);
    }
    printf("hello usb done\n");
    while (true) {
        tight_loop_contents();
    }
}
