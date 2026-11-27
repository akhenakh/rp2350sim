cmake_minimum_required(VERSION 3.13)

if (NOT DEFINED ENV{PICO_SDK_PATH})
    message(FATAL_ERROR "set PICO_SDK_PATH to the pico-sdk checkout")
endif()

include($ENV{PICO_SDK_PATH}/pico_sdk_init.cmake)

project(rp2350sim_ble C CXX ASM)
set(CMAKE_C_STANDARD 11)

pico_sdk_init()

# Stock Pico W BTstack GATT client. It scans for the Environmental Sensing
# service, connects, discovers the temperature characteristic, enables
# notifications and prints the values. The emulator's virtual BLE controller
# answers below the CYW43 HCI boundary.
add_executable(ble_temp_reader client.c)
target_link_libraries(ble_temp_reader
    pico_stdlib
    pico_btstack_ble
    pico_btstack_cyw43
    pico_cyw43_arch_none
    )
target_include_directories(ble_temp_reader PRIVATE ${CMAKE_CURRENT_LIST_DIR})

pico_enable_stdio_uart(ble_temp_reader 1)
pico_enable_stdio_usb(ble_temp_reader 0)

pico_add_extra_outputs(ble_temp_reader)
