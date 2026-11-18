// Pico SDK GPIO example: 5 Hz blink on GP25 (the Pico 2 on-board LED).
#include "pico/stdlib.h"

int main(void) {
    const uint LED_PIN = 25;
    gpio_init(LED_PIN);
    gpio_set_dir(LED_PIN, GPIO_OUT);
    while (true) {
        gpio_put(LED_PIN, 1);
        sleep_ms(100);
        gpio_put(LED_PIN, 0);
        sleep_ms(100);
    }
}
