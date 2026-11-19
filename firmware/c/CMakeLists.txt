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

add_executable(blinky blinky/main.c)
target_link_libraries(blinky pico_stdlib)

add_executable(floatdemo floatdemo/main.c)
target_link_libraries(floatdemo pico_stdlib)

add_executable(multicore multicore/main.c)
target_link_libraries(multicore pico_stdlib pico_multicore)

add_executable(otp otp/main.c)
target_link_libraries(otp pico_stdlib)

foreach(app hello blinky floatdemo multicore otp)
    pico_enable_stdio_uart(${app} 1)
    pico_enable_stdio_usb(${app} 0)
    pico_add_extra_outputs(${app})
endforeach()
