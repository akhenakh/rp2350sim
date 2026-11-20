// psram checks the RP2350 QMI window-1 device: the SDK initialisation, the
// reported size, a write/read pattern over 1 MiB and the cost of uncached
// accesses relative to SRAM.
//
// Build with PICO_PSRAM_CS_PIN=8 and an 8 MiB PICO_PSRAM_SIZE_BYTES; the
// emulator exposes the same device with -psram 8 (or the pico2-psram board).

#include <stdio.h>
#include <string.h>

#include "hardware/psram.h"
#include "pico/stdlib.h"
#include "pico/time.h"

#define PSRAM_WORDS (256 * 1024) // 1 MiB
#define PSRAM_READ_PASSES 8

int main(void) {
    stdio_init_all();
    sleep_ms(200);

    volatile uint32_t *psram = (volatile uint32_t *)0x11000000u;
    const size_t size = psram_get_size();
    printf("psram: available=%d size=%u\n", psram_is_available() ? 1 : 0, (unsigned)size);
    if (!psram_is_available() || size < 8u * 1024u * 1024u) {
        printf("psram: FAIL\n");
        return 1;
    }

    for (uint32_t i = 0; i < PSRAM_WORDS; i++) {
        psram[i] = i * 2654435761u;
    }
    uint32_t bad = 0;
    for (uint32_t i = 0; i < PSRAM_WORDS; i++) {
        if (psram[i] != i * 2654435761u) {
            bad++;
        }
    }

    const uint64_t start = time_us_64();
    volatile uint32_t sum = 0;
    for (int pass = 0; pass < PSRAM_READ_PASSES; pass++) {
        for (uint32_t i = 0; i < PSRAM_WORDS; i++) {
            sum += psram[i];
        }
    }
    const uint64_t elapsed = time_us_64() - start;

    printf("psram: ok size=%u words=%u bad=%u read_us=%llu sum=%08x\n",
           (unsigned)size, (unsigned)PSRAM_WORDS, (unsigned)bad,
           (unsigned long long)elapsed, (unsigned)sum);
    if (bad != 0) {
        return 1;
    }
    while (true) {
        tight_loop_contents();
    }
}
