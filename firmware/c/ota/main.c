// Single-slot self-update demo. The new firmware is embedded in this image,
// copied to SRAM, and written over the running image from RAM-resident code.
//
// The ROM function pointers are resolved before flash is erased: the SDK's
// rom_* inline wrappers call flash-resident lookup helpers, which would be
// gone after the erase.
#include <stdio.h>
#include <string.h>

#include "hardware/flash.h"
#include "pico/bootrom.h"
#include "pico/stdlib.h"

extern const uint8_t ota2_bin[];
extern const uint32_t ota2_bin_len;

typedef void (*rom_void_fn)(void);
typedef void (*rom_erase_fn)(uint32_t, size_t, uint32_t, uint8_t);
typedef void (*rom_program_fn)(uint32_t, const uint8_t *, size_t);
typedef int (*rom_reboot_fn)(uint32_t, uint32_t, uint32_t, uint32_t);

static rom_void_fn g_connect;
static rom_void_fn g_exit_xip;
static rom_erase_fn g_erase;
static rom_program_fn g_program;
static rom_void_fn g_flush;
static rom_void_fn g_enter_xip;
static rom_reboot_fn g_reboot;

static uint8_t staging[64 * 1024] __attribute__((aligned(4)));

// This function must not execute from flash once flash_range_erase has erased
// the running image.
static void __no_inline_not_in_flash_func(run_update)(uint32_t len) {
    uint32_t program_len = (len + FLASH_PAGE_SIZE - 1) & ~(FLASH_PAGE_SIZE - 1);
    uint32_t erase_len = (len + FLASH_SECTOR_SIZE - 1) & ~(FLASH_SECTOR_SIZE - 1);
    g_connect();
    g_exit_xip();
    g_erase(0, erase_len, FLASH_SECTOR_SIZE, 0x20);
    g_program(0, staging, program_len);
    g_flush();
    g_enter_xip();
    g_reboot(0, 0, 0, 0);
    for (;;) {
        tight_loop_contents();
    }
}

int main(void) {
    stdio_init_all();
    printf("ota v1: image %u bytes\n", ota2_bin_len);

    g_connect = (rom_void_fn)rom_func_lookup_inline(ROM_FUNC_CONNECT_INTERNAL_FLASH);
    g_exit_xip = (rom_void_fn)rom_func_lookup_inline(ROM_FUNC_FLASH_EXIT_XIP);
    g_erase = (rom_erase_fn)rom_func_lookup_inline(ROM_FUNC_FLASH_RANGE_ERASE);
    g_program = (rom_program_fn)rom_func_lookup_inline(ROM_FUNC_FLASH_RANGE_PROGRAM);
    g_flush = (rom_void_fn)rom_func_lookup_inline(ROM_FUNC_FLASH_FLUSH_CACHE);
    g_enter_xip = (rom_void_fn)rom_func_lookup_inline(ROM_FUNC_FLASH_ENTER_CMD_XIP);
    g_reboot = (rom_reboot_fn)rom_func_lookup_inline(ROM_FUNC_REBOOT);

    if (!g_connect || !g_exit_xip || !g_erase || !g_program || !g_flush || !g_enter_xip || !g_reboot) {
        printf("ota v1: missing ROM service\n");
        return 1;
    }
    if (ota2_bin_len > sizeof(staging)) {
        printf("ota v1: image too large\n");
        return 1;
    }

    memset(staging, 0xFF, sizeof(staging));
    memcpy(staging, ota2_bin, ota2_bin_len);
    printf("ota v1: updating\n");
    run_update(ota2_bin_len);
}
