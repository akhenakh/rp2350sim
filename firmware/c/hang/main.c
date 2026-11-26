#include <stdio.h>
// Deliberate tight poll loop for the emulator's hang detector. PLL_USB is not
// configured by the SDK, so its PWR.PD is asserted at reset and CS.LOCK never
// sets. The emulator should pause and explain the register/bit.
#include "pico/stdlib.h"

int main(void) {
    stdio_init_all();
    // Force PLL_SYS powerdown so LOCK can never set, then poll it. This is
    // the shape of the real "PD still asserted" bring-up hang.
    volatile uint32_t *pwr = (volatile uint32_t *)0x40050004u; // PLL_SYS PWR
    *pwr = 0x2du;                                             // PD|VCOPD|...
    volatile uint32_t *cs = (volatile uint32_t *)0x40050000u; // PLL_SYS CS
    printf("pwr=%08lx cs=%08lx\n", (unsigned long)*pwr, (unsigned long)*cs);
    while ((*cs & (1u << 31)) == 0) {
        tight_loop_contents();
    }
    printf("pll usb locked\n");
    return 0;
}
