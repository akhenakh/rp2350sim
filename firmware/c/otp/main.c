/**
 * OTP demo for rp2350sim: exercises the bootrom otp_access service, ECC
 * encoding/correction, fuse (bit-set-only) semantics and page locking.
 *
 * Derived from the pico-examples hello_otp example (BSD-3-Clause,
 * Copyright (c) 2020 Raspberry Pi (Trading) Ltd.).
 */

#include <stdio.h>
#include <string.h>

#include "hardware/structs/otp.h"
#include "pico/bootrom.h"
#include "pico/stdlib.h"

#define ECC_ROW 0x400
#define RAW_ROW 0x410

static int failures;

static void check(const char *what, bool ok, int code) {
    if (ok) {
        printf("ok   %s\n", what);
    } else {
        printf("FAIL %s (code %d)\n", what, code);
        failures++;
    }
}

static int otp_read(void *buf, uint32_t len, uint32_t row, bool ecc) {
    otp_cmd_t cmd = {.flags = row | (ecc ? OTP_CMD_ECC_BITS : 0)};
    return rom_func_otp_access(buf, len, cmd);
}

static int otp_write(const void *buf, uint32_t len, uint32_t row, bool ecc) {
    otp_cmd_t cmd = {.flags = row | OTP_CMD_WRITE_BITS | (ecc ? OTP_CMD_ECC_BITS : 0)};
    return rom_func_otp_access((uint8_t *)buf, len, cmd);
}

int main(void) {
    stdio_init_all();

    // The demo needs empty rows so it is re-runnable on a fresh device.
    uint8_t probe[32] = {0};
    if (otp_read(probe, sizeof(probe) / 2, ECC_ROW, true) != 0 ||
        otp_read(probe + 16, sizeof(probe) / 2, RAW_ROW, false) != 0) {
        printf("OTP demo: cannot read rows\n");
        return 1;
    }
    for (unsigned i = 0; i < sizeof(probe); i++) {
        if (probe[i] != 0) {
            printf("OTP demo: rows %#x/%#x are not blank, edit ECC_ROW/RAW_ROW\n", ECC_ROW, RAW_ROW);
            return 1;
        }
    }
    printf("OTP demo: rows blank, chip id readable\n");

    // ECC-encoded write, then read it back.
    const char *text = "Hello from OTP";
    uint8_t ecc_write[16] = {0};
    memcpy(ecc_write, text, strlen(text));
    check("ECC write", otp_write(ecc_write, sizeof(ecc_write), ECC_ROW, true) == 0, 0);

    uint8_t ecc_read[16] = {0};
    check("ECC read", otp_read(ecc_read, sizeof(ecc_read), ECC_ROW, true) == 0, 0);
    check("ECC data", memcmp(ecc_read, text, strlen(text)) == 0, 0);
    printf("     \"%s\"\n", ecc_read);

    // Raw write: 3 meaningful bytes per 4-byte stride.
    uint8_t raw_write[20] = "Hel\0lo \0fro\0m O\0TP";
    check("raw write", otp_write(raw_write, sizeof(raw_write), RAW_ROW, false) == 0, 0);

    uint8_t raw_read[20] = {0};
    check("raw read", otp_read(raw_read, sizeof(raw_read), RAW_ROW, false) == 0, 0);
    uint8_t compact[15];
    for (unsigned i = 0; i < 5; i++) {
        memcpy(compact + i * 3, raw_read + i * 4, 3);
    }
    check("raw data", memcmp(compact, text, strlen(text)) == 0, 0);

    // Fuse semantics: only set bits, never clear them.
    uint8_t raw_more[20] = {0};
    for (unsigned i = 0; i < sizeof(raw_write); i++) {
        if (raw_write[i]) {
            raw_more[i] = (raw_write[i] % 2) ? 'o' : '~';
        }
    }
    check("raw overwrite (set bits)", otp_write(raw_more, sizeof(raw_more), RAW_ROW, false) == 0, 0);

    uint8_t ecc_again[16] = "hello from otp";
    int code = otp_write(ecc_again, sizeof(ecc_again), ECC_ROW, true);
    check("ECC overwrite refused", code == BOOTROM_ERROR_UNSUPPORTED_MODIFICATION, code);

    // Corrupt one bit of an ECC row through the raw alias: the ECC alias
    // must still return the original data.
    uint8_t raw_ecc[32] = {0};
    check("raw read of ECC rows", otp_read(raw_ecc, sizeof(raw_ecc), ECC_ROW, false) == 0, 0);
    raw_ecc[24] = 't'; // row 6 was 'T'; one bit differs, correctable
    check("raw corruption write", otp_write(raw_ecc, sizeof(raw_ecc), ECC_ROW, false) == 0, 0);

    uint8_t corrected[16] = {0};
    check("ECC read after corruption", otp_read(corrected, sizeof(corrected), ECC_ROW, true) == 0, 0);
    check("single-bit error corrected", memcmp(corrected, text, strlen(text)) == 0, 0);

    // Software-lock the page: reads and writes must now be refused until the
    // next reset.
    otp_hw->sw_lock[ECC_ROW / 0x40] = 0xf;
    printf("     page %d software locked\n", ECC_ROW / 0x40);
    code = otp_read(corrected, sizeof(corrected), ECC_ROW, true);
    check("locked read refused", code == BOOTROM_ERROR_NOT_PERMITTED, code);
    code = otp_write(ecc_again, sizeof(ecc_again), ECC_ROW, true);
    check("locked write refused", code == BOOTROM_ERROR_NOT_PERMITTED, code);

    if (failures == 0) {
        printf("OTP demo: ok\n");
    } else {
        printf("OTP demo: %d failure(s)\n", failures);
    }

    // Keep the machine alive so the OTP tab stays visible in the UI.
    gpio_init(25);
    gpio_set_dir(25, GPIO_OUT);
    bool led = false;
    while (true) {
        gpio_put(25, led = !led);
        sleep_ms(500);
    }
}
