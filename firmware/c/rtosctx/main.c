// A minimal preemptive kernel that exercises the RTOS-facing Cortex-M33
// features: SVC first-task launch on PSP, SysTick preemption, PendSV context
// switching, BASEPRI critical sections and nested interrupts.
//
// It is not FreeRTOS: two hand-built task frames are enough to prove the
// exception model end to end without pulling in a kernel.

#include <stdint.h>
#include <stdio.h>

#include "hardware/irq.h"
#include "hardware/structs/systick.h"
#include "pico/stdlib.h"

#define REG32(addr) (*(volatile uint32_t *)(addr))
#define REG8(addr) (*(volatile uint8_t *)(addr))

#define M33_ICSR 0xE000ED04u
#define M33_SHPR2_3 0xE000ED1Fu // SVCall priority byte
#define M33_SHPR3_2 0xE000ED22u // PendSV priority byte
#define M33_SHPR3_3 0xE000ED23u // SysTick priority byte

#define STACK_WORDS 512

static uint32_t stack_a[STACK_WORDS] __attribute__((aligned(8)));
static uint32_t stack_b[STACK_WORDS] __attribute__((aligned(8)));

uint32_t *saved_sp[2];
volatile int current;
volatile int next_task = -1;
static volatile uint32_t tick_count;
static volatile uint32_t a_count;
static volatile uint32_t b_count;
static volatile int a_done;
static volatile int b_done;
static volatile int nested_seen;
static volatile int nested_ok;
static volatile int crit_ok;

// make_stack builds the frame a PendSV load or an SVC launch expects: eight
// software-saved callee registers below the hardware exception frame.
static uint32_t *make_stack(uint32_t *top, void (*entry)(void)) {
    uint32_t *sp = top;
    *--sp = 0x01000000;      // xPSR: Thumb, thread mode
    *--sp = (uint32_t)entry; // PC
    *--sp = 0;               // LR
    *--sp = 0;               // R12
    *--sp = 0;               // R3
    *--sp = 0;               // R2
    *--sp = 0;               // R1
    *--sp = 0;               // R0
    for (int i = 0; i < 8; i++) {
        *--sp = 0; // R4-R11
    }
    return sp;
}

// isr_svcall launches the first task: load R4-R11 and PSP from the built
// frame, then return as if the thread had been entered with EXC_RETURN=thread,
// PSP (0xFFFFFFFD).
__attribute__((naked)) void isr_svcall(void) {
    __asm volatile(
        "ldr r0, =saved_sp\n"
        "ldr r0, [r0]\n"
        "ldmia r0!, {r4-r11}\n"
        "msr psp, r0\n"
        "ldr r1, =0xFFFFFFFD\n"
        "mov lr, r1\n"
        "bx lr\n");
}

// isr_pendsv saves the outgoing task's R4-R11 under its hardware frame and
// resumes the incoming one.
__attribute__((naked)) void isr_pendsv(void) {
    __asm volatile(
        "mrs r0, psp\n"
        "stmdb r0!, {r4-r11}\n"
        "ldr r1, =saved_sp\n"
        "ldr r2, =current\n"
        "ldr r3, [r2]\n"
        "str r0, [r1, r3, lsl #2]\n"
        "ldr r4, =next_task\n"
        "ldr r4, [r4]\n"
        "str r4, [r2]\n"
        "ldr r0, [r1, r4, lsl #2]\n"
        "ldmia r0!, {r4-r11}\n"
        "msr psp, r0\n"
        "bx lr\n");
}

// isr_irq0 is the higher-priority nested interrupt: SysTick pends it while it
// runs and checks that it preempted.
void isr_irq0(void) { nested_seen = 1; }

void isr_systick(void) {
    tick_count++;
    if (tick_count == 5) {
        nested_seen = 0;
        irq_set_pending(0); // priority 0x00 beats SysTick's 0x80
        nested_ok = nested_seen;
    }
    next_task = current ^ 1;
    REG32(M33_ICSR) = 1u << 28; // PENDSVSET
}

static void crit_test(void) {
    uint32_t before = tick_count;
    uint32_t start = time_us_32();
    __asm volatile("msr basepri, %0" ::"r"(0x40) : "memory");
    while (time_us_32() - start < 2000) {
        // BASEPRI=0x40 masks SysTick (0x80): no tick may be delivered.
    }
    // Sample before unmasking: the pent-up tick is delivered as soon as
    // BASEPRI is cleared, which is correct hardware behaviour.
    uint32_t after = tick_count;
    __asm volatile("msr basepri, %0" ::"r"(0) : "memory");
    crit_ok = after == before;
}

static void task_a(void) {
    uint32_t last = tick_count;
    for (int n = 0; n < 5; n++) {
        while (tick_count == last) {
        }
        last = tick_count;
        a_count++;
        if (n == 2) {
            crit_test();
        }
    }
    a_done = 1;
    while (!b_done) {
    }
    printf("rtosctx ok a=%u b=%u nested=%d crit=%d\n", (unsigned)a_count,
           (unsigned)b_count, nested_ok, crit_ok);
    for (;;) {
        __asm volatile("wfi");
    }
}

static void task_b(void) {
    uint32_t last = tick_count;
    for (int n = 0; n < 5; n++) {
        while (tick_count == last) {
        }
        last = tick_count;
        b_count++;
    }
    b_done = 1;
    for (;;) {
        __asm volatile("wfi");
    }
}

int main(void) {
    stdio_init_all();

    REG8(M33_SHPR2_3) = 0x00; // SVCall highest
    REG8(M33_SHPR3_2) = 0x80; // PendSV
    REG8(M33_SHPR3_3) = 0x80; // SysTick
    irq_set_priority(0, 0x00);
    irq_set_enabled(0, true);

    saved_sp[0] = make_stack(stack_a + STACK_WORDS, task_a);
    saved_sp[1] = make_stack(stack_b + STACK_WORDS, task_b);
    current = 0;

    systick_hw->rvr = 150000 - 1; // 1 ms at 150 MHz
    systick_hw->cvr = 0;
    systick_hw->csr = 7; // CPU clock, interrupt, enable

    __asm volatile("svc #0");

    for (;;) {
        tight_loop_contents();
    }
}
