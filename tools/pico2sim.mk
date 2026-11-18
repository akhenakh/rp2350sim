# Drop-in Makefile for a TinyGo project:
#
#   include path/to/rp2350sim/tools/pico2sim.mk
#
# Targets: pico2-build, pico2-run, pico2-ci. Override the variables on the
# command line, e.g. `make pico2-run RP2350SIM_SECONDS=5`.

TINYGO            ?= tinygo
PICO2_TARGET      ?= pico2
PICO2_FIRMWARE    ?= build/firmware.elf
RP2350SIM         ?= rp2350sim
RP2350SIM_SECONDS ?= 3

.PHONY: pico2-build pico2-run pico2-ci

pico2-build:
	@mkdir -p $(dir $(PICO2_FIRMWARE))
	$(TINYGO) build -target=$(PICO2_TARGET) -o $(PICO2_FIRMWARE) .

pico2-run: pico2-build
	$(RP2350SIM) -fw $(PICO2_FIRMWARE) -seconds $(RP2350SIM_SECONDS)

pico2-ci: pico2-build
	$(RP2350SIM) -fw $(PICO2_FIRMWARE) -seconds $(RP2350SIM_SECONDS) \
		-quiet -summary-file $(PICO2_FIRMWARE).json
