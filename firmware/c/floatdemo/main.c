// Single-precision FPU exercise. Prints bit patterns so the result does not
// depend on printf's floating-point formatting.
#include <stdio.h>
#include "pico/stdlib.h"

volatile float a = 1.5f, b = 2.25f;

static unsigned bits(float f) {
    union { float f; unsigned u; } x;
    x.f = f;
    return x.u;
}

int main(void) {
    stdio_init_all();
    float sum = a + b, prod = a * b, quot = b / a, diff = b - a;
    printf("float %08x %08x %08x %08x\n", bits(sum), bits(prod), bits(quot), bits(diff));
    printf("float cmp %d %d %d\n", sum > diff, sum == 3.75f, a < b);
    while (true) {
        tight_loop_contents();
    }
}
