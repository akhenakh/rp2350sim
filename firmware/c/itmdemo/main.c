// Write a string to ITM stimulus port 0. The emulator's -itm flag prints it;
// no UART or USB stdio is involved.
#include "pico/stdlib.h"

#define ITM_STIM0 (*(volatile unsigned int *)0xE0000000)
#define ITM_TER   (*(volatile unsigned int *)0xE0000E00)
#define ITM_TCR   (*(volatile unsigned int *)0xE0000E80)

int main(void) {
    ITM_TER = 1;
    ITM_TCR = 1;
    for (;;) {
        const char *s = "itm: hello from ITM\n";
        for (const char *p = s; *p; p++) {
            while (ITM_STIM0 == 0) {
            }
            ITM_STIM0 = (unsigned char)*p;
        }
        sleep_ms(200);
    }
}
