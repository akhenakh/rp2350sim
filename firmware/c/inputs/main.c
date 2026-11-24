// Board-controls demo for the pico2-inputs board.
//
// Reads the pushbutton (GP15), slide switch (GP14), rotary encoder (GP12/13),
// ADC pot (GP26/ADC0) and the BOOTSEL button (QSPI_CSN in SIO_GPIO_HI_IN), and
// reports changes over UART. GP25 mirrors the button/switch so the board
// diagram shows activity while the controls are clicked in the UI.

#include <stdio.h>

#include "hardware/adc.h"
#include "hardware/gpio.h"
#include "hardware/regs/sio.h"
#include "hardware/structs/sio.h"
#include "pico/stdlib.h"

#define BTN_PIN 15
#define SW_PIN 14
#define ENC_A 12
#define ENC_B 13
#define POT_ADC 0
#define POT_GPIO 26
#define LED_PIN 25

// Quadrature transition table indexed by (prev << 2) | current, A in bit 1
// and B in bit 0. Idle is 11; the table yields +1 or -1 per valid step.
static const int8_t enc_steps[16] = {
	0, -1, 1, 0,
	1, 0, 0, -1,
	-1, 0, 0, 1,
	0, 1, -1, 0,
};

static void input_pin(uint pin) {
	gpio_init(pin);
	gpio_set_dir(pin, GPIO_IN);
	gpio_pull_up(pin);
}

static int pot_delta(uint16_t a, uint16_t b) {
	return a > b ? a - b : b - a;
}

int main(void) {
	stdio_init_all();

	input_pin(BTN_PIN);
	input_pin(SW_PIN);
	input_pin(ENC_A);
	input_pin(ENC_B);

	gpio_init(LED_PIN);
	gpio_set_dir(LED_PIN, GPIO_OUT);
	gpio_put(LED_PIN, 0);

	adc_init();
	adc_gpio_init(POT_GPIO);
	adc_select_input(POT_ADC);

	uint8_t enc_prev = (gpio_get(ENC_A) << 1) | gpio_get(ENC_B);
	int enc_count = 0;
	int enc_count_prev = 0;
	bool btn_prev = !gpio_get(BTN_PIN);
	bool sw_prev = !gpio_get(SW_PIN);
	bool boot_prev = (sio_hw->gpio_hi_in & SIO_GPIO_HI_IN_QSPI_CSN_BITS) == 0;
	uint16_t pot_prev = adc_read();

	printf("inputs demo: use the Board tab controls\n");
	printf("inputs: btn=%d sw=%d enc=%d boot=%d pot=%4u (%.2f V)\n",
	       btn_prev, sw_prev, enc_count, boot_prev, pot_prev,
	       pot_prev * 3.3f / 4095.0f);

	absolute_time_t next_beat = make_timeout_time_ms(1000);
	for (;;) {
		bool btn = !gpio_get(BTN_PIN);
		bool sw = !gpio_get(SW_PIN);
		bool boot = (sio_hw->gpio_hi_in & SIO_GPIO_HI_IN_QSPI_CSN_BITS) == 0;
		uint8_t enc = (gpio_get(ENC_A) << 1) | gpio_get(ENC_B);
		enc_count += enc_steps[(enc_prev << 2) | enc];
		enc_prev = enc;
		uint16_t pot = adc_read();

		gpio_put(LED_PIN, btn || sw);

		// Ignore ADC jitter of a few LSBs; the pot is only a demo.
		bool changed = btn != btn_prev || sw != sw_prev || boot != boot_prev ||
			       enc_count != enc_count_prev || pot_delta(pot, pot_prev) > 8;
		if (changed || time_reached(next_beat)) {
			printf("inputs: btn=%d sw=%d enc=%d boot=%d pot=%4u (%.2f V)\n",
			       btn, sw, enc_count, boot, pot, pot * 3.3f / 4095.0f);
			btn_prev = btn;
			sw_prev = sw;
			boot_prev = boot;
			pot_prev = pot;
			enc_count_prev = enc_count;
			next_beat = make_timeout_time_ms(1000);
		}
		sleep_ms(10);
	}
}
