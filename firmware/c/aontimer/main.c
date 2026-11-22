// pico_aon_timer demo: set the always-on timer, arm an alarm and poll until it
// fires. Verifies the POWMAN millisecond counter used by sleep/wake code.
#include <stdio.h>
#include "pico/aon_timer.h"
#include "pico/stdlib.h"

int main(void) {
    stdio_init_all();
    struct timespec start = {.tv_sec = 1000, .tv_nsec = 0};
    aon_timer_start(&start);

    struct timespec now;
    aon_timer_get_time(&now);
    printf("aon start %lld\n", (long long)now.tv_sec);

    struct timespec alarm = {.tv_sec = 1001, .tv_nsec = 0};
    aon_timer_enable_alarm(&alarm, NULL, false);

    while (true) {
        aon_timer_get_time(&now);
        if (now.tv_sec >= 1001) {
            break;
        }
        sleep_ms(100);
    }
    printf("aon alarm %lld\n", (long long)now.tv_sec);
}
