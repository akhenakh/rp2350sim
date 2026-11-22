// Double-precision DCP exercise. The Pico SDK routes __aeabi_dadd/dsub/dmul/
// ddiv and sqrt through the RP2350 double coprocessor, so these results cover
// the canned sequences, not just the dot-product path.
#include <math.h>
#include <stdio.h>
#include <string.h>

#include "pico/stdlib.h"

volatile double a = 1.0, b = 3.0, c = 2.0, d = -5.5;

static unsigned long long bits(double x) {
    unsigned long long u;
    memcpy(&u, &x, sizeof u);
    return u;
}

int main(void) {
    stdio_init_all();
    double sum = a + b;
    double diff = d - c;
    double prod = b * c;
    double quot = a / b;
    double root = sqrt(c);
    printf("dcp %016llx %016llx %016llx %016llx %016llx\n",
           bits(sum), bits(diff), bits(prod), bits(quot), bits(root));
    printf("dcp ok\n");
    while (true) {
        tight_loop_contents();
    }
}
