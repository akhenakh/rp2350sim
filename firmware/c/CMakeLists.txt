cmake_minimum_required(VERSION 3.13)

if (NOT DEFINED ENV{PICO_SDK_PATH})
    message(FATAL_ERROR "set PICO_SDK_PATH to the pico-sdk checkout")
endif()

include($ENV{PICO_SDK_PATH}/pico_sdk_init.cmake)

project(rp2350sim_c C CXX ASM)
set(CMAKE_C_STANDARD 11)

pico_sdk_init()

add_executable(hello hello/main.c)
target_link_libraries(hello pico_stdlib)

add_executable(usbhello usbhello/main.c)
target_link_libraries(usbhello pico_stdlib)

add_executable(usbecho usbecho/main.c)
target_link_libraries(usbecho pico_stdlib)

add_executable(aontimer aontimer/main.c)
target_link_libraries(aontimer pico_stdlib pico_aon_timer)

add_executable(rtosctx rtosctx/main.c)
target_link_libraries(rtosctx pico_stdlib)

add_executable(inputs inputs/main.c)
target_link_libraries(inputs pico_stdlib hardware_adc)

add_executable(blinky blinky/main.c)
target_link_libraries(blinky pico_stdlib)

add_executable(floatdemo floatdemo/main.c)
target_link_libraries(floatdemo pico_stdlib)

add_executable(dcp dcp/main.c)
target_link_libraries(dcp pico_stdlib)

add_executable(multicore multicore/main.c)
target_link_libraries(multicore pico_stdlib pico_multicore)

add_executable(otp otp/main.c)
target_link_libraries(otp pico_stdlib)

add_executable(debug debug/main.c)
target_link_libraries(debug pico_stdlib)

add_executable(hang hang/main.c)
target_link_libraries(hang pico_stdlib)

add_executable(psram psram/main.c)
target_link_libraries(psram pico_stdlib hardware_psram)
pico_override_psram_size(psram 8388608)
target_compile_definitions(psram PRIVATE PICO_PSRAM_CS_PIN=8)

add_executable(ota2 ota2/main.c)
target_link_libraries(ota2 pico_stdlib)

set(OTA2_BIN ${CMAKE_CURRENT_BINARY_DIR}/ota2.bin)
set(OTA2_IMAGE_C ${CMAKE_CURRENT_BINARY_DIR}/ota2_image.c)
add_custom_command(
    OUTPUT ${OTA2_IMAGE_C}
    COMMAND python3 ${CMAKE_CURRENT_SOURCE_DIR}/bin2c.py ${OTA2_BIN} ${OTA2_IMAGE_C} ota2_bin
    DEPENDS ota2 ${OTA2_BIN}
    COMMENT "Embedding ota2.bin in the OTA updater"
    VERBATIM)
set_source_files_properties(${OTA2_IMAGE_C} PROPERTIES GENERATED TRUE)

add_executable(ota ota/main.c ${OTA2_IMAGE_C})
target_link_libraries(ota pico_stdlib hardware_flash)
add_dependencies(ota ota2)

foreach(app hello blinky floatdemo dcp multicore otp psram debug hang ota ota2 rtosctx inputs)
    pico_enable_stdio_uart(${app} 1)
    pico_enable_stdio_usb(${app} 0)
    pico_add_extra_outputs(${app})
endforeach()

# TinyUSB CDC stdio: exercises a descriptor-driven USB host (pico_enable_stdio_usb).
pico_enable_stdio_uart(usbhello 0)
pico_enable_stdio_usb(usbhello 1)
pico_add_extra_outputs(usbhello)

pico_enable_stdio_uart(usbecho 0)
pico_enable_stdio_usb(usbecho 1)
pico_add_extra_outputs(usbecho)

pico_enable_stdio_uart(aontimer 1)
pico_enable_stdio_usb(aontimer 0)
pico_add_extra_outputs(aontimer)
