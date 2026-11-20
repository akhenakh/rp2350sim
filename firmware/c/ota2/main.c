// The image the single-slot OTA updater installs. It messages the UART so the
// emulator's smoke test can prove the reset booted the new firmware.
#include <stdio.h>

#include "pico/stdlib.h"

int main(void) {
    stdio_init_all();
    gpio_init(25);
    gpio_set_dir(25, GPIO_OUT);
    printf("ota v2 running\n");
    bool on = false;
    while (true) {
        gpio_put(25, on = !on);
        sleep_ms(500);
    }
}
