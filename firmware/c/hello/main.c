// Stock Pico SDK hello world: integer output over UART0.
#include <stdio.h>
#include "pico/stdlib.h"

int main(void) {
    stdio_init_all();
    for (int i = 0; i < 3; i++) {
        printf("hello c %d\n", i);
        sleep_ms(50);
    }
    printf("hello c done\n");
    while (true) {
        tight_loop_contents();
    }
}
