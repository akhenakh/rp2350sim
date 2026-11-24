package main

import (
	"os"
	"testing"
)

// TestTouchPointRotation checks the LCD click mapping for the Touch-LCD-2's
// 90-degree rotation: the displayed 320x240 frame runs along the panel's
// native Y axis, and MX mirrors native X.
func TestTouchPointRotation(t *testing.T) {
	b := &BoardDef{
		LCD:   &LCDDevice{Width: 240, Height: 320, Rotation: 90},
		Touch: &TouchDevice{Width: 240, Height: 320},
	}
	cases := []struct {
		fx, fy float32
		x, y   int
	}{
		{0, 0, 239, 0},
		{1, 1, 0, 319},
		{0.5, 0.5, 119, 160},
	}
	for _, c := range cases {
		x, y := touchPoint(b, c.fx, c.fy, 320, 240)
		if x != c.x || y != c.y {
			t.Errorf("touchPoint(%v,%v) = %d,%d, want %d,%d", c.fx, c.fy, x, y, c.x, c.y)
		}
	}
}

// TestBoardsIncludeTouchLCD2 guards the embedded board list and the GEEK card
// bus (the LCD is SPI1, the card is SPI0).
func TestBoardsIncludeTouchLCD2(t *testing.T) {
	boards, err := LoadBoards()
	if err != nil {
		t.Fatal(err)
	}
	var touch, geek *BoardDef
	for i := range boards {
		switch boards[i].ID {
		case "touch-lcd2":
			touch = &boards[i]
		case "geek":
			geek = &boards[i]
		}
	}
	if touch == nil {
		t.Fatal("touch-lcd2 board is missing")
	}
	if touch.LCD == nil || touch.LCD.Rotation != 90 || touch.LCD.BacklightPin() != 15 {
		t.Fatalf("touch-lcd2 LCD = %+v", touch.LCD)
	}
	// The YAML is the logical post-rotation frame. The native panel is
	// 240x320 portrait; rotation 90 must display the runner's 320x240.
	if touch.LCD.Width != 320 || touch.LCD.Height != 240 {
		t.Fatalf("touch-lcd2 logical LCD = %dx%d, want 320x240", touch.LCD.Width, touch.LCD.Height)
	}
	if touch.Touch == nil || touch.Touch.Addr != 0x15 || touch.Touch.RSTPin() != 20 {
		t.Fatalf("touch-lcd2 touch = %+v", touch.Touch)
	}
	if touch.IMU == nil || touch.IMU.Addr != 0x6b {
		t.Fatalf("touch-lcd2 imu = %+v", touch.IMU)
	}
	if touch.Battery == nil || touch.Battery.Divider != 3 {
		t.Fatalf("touch-lcd2 battery = %+v", touch.Battery)
	}
	if touch.SD == nil || touch.SD.SPI != 1 || touch.SD.CS != 25 {
		t.Fatalf("touch-lcd2 sd = %+v", touch.SD)
	}
	if geek == nil || geek.SD == nil || geek.SD.SPI != 0 || geek.SD.CS != 23 {
		t.Fatalf("geek sd = %+v", geek)
	}
}

// writeBoards writes a temporary board file and points LoadBoards at it.
func writeBoards(t *testing.T, yaml string) {
	t.Helper()
	path := t.TempDir() + "/boards.yaml"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RP2350SIM_BOARDS", path)
}

// TestBoardsAllowSharedSPI checks that an LCD and an Ethernet controller may
// share a controller when they have different chip selects.
func TestBoardsAllowSharedSPI(t *testing.T) {
	writeBoards(t, `boards:
  - id: shared
    lcd: { spi: 0, cs: 17, dc: 16, rst: 20, width: 240, height: 320 }
    ethernet: { kind: w5500, spi: 0, cs: 20 }
`)
	if _, err := LoadBoards(); err != nil {
		t.Fatalf("shared bus rejected: %v", err)
	}
}

// TestBoardsRejectSameChipSelect checks the real conflict: one CS on one bus.
func TestBoardsRejectSameChipSelect(t *testing.T) {
	writeBoards(t, `boards:
  - id: clash
    lcd: { spi: 0, cs: 17, dc: 16, rst: 20, width: 240, height: 320 }
    ethernet: { kind: w5500, spi: 0, cs: 17 }
`)
	if _, err := LoadBoards(); err == nil {
		t.Fatal("same CS on one bus was accepted")
	}
}

// TestLCDAspectUsesLogicalSize checks the pre-frame placeholder uses the
// YAML's post-rotation size, matching the framebuffer once firmware runs.
func TestLCDAspectUsesLogicalSize(t *testing.T) {
	old := selectedBoard
	defer func() { selectedBoard = old }()
	selectedBoard = &BoardDef{LCD: &LCDDevice{Width: 320, Height: 240, Rotation: 90}}
	if w, h := lcdAspect(MachineState{}); w != 320 || h != 240 {
		t.Fatalf("lcdAspect = %dx%d, want 320x240", w, h)
	}
}

// TestBoardsRejectSharedEthernetUART checks that CH9120 and Wi-Fi cannot
// claim the same hardware UART.
func TestBoardsRejectSharedEthernetUART(t *testing.T) {
	writeBoards(t, `boards:
  - id: uart-clash
    ethernet: { kind: ch9120, uart: 1 }
    wifi: { uart: 1 }
`)
	if _, err := LoadBoards(); err == nil {
		t.Fatal("CH9120 and Wi-Fi accepted the same UART")
	}
}

func TestBoardsIncludeBus(t *testing.T) {
	boards, err := LoadBoards()
	if err != nil {
		t.Fatal(err)
	}
	var bus *BoardDef
	for i := range boards {
		if boards[i].ID == "pico2-bus" {
			bus = &boards[i]
		}
	}
	if bus == nil {
		t.Fatal("pico2-bus board is missing")
	}
	if len(bus.I2CTCP) != 1 || bus.I2CTCP[0].Bus != 0 ||
		len(bus.I2CTCP[0].Addrs) != 1 || bus.I2CTCP[0].Addrs[0] != 0x50 {
		t.Fatalf("pico2-bus i2c_tcp = %+v", bus.I2CTCP)
	}
	if len(bus.SPITCP) != 1 || bus.SPITCP[0].Bus != 0 || bus.SPITCP[0].CS != 17 {
		t.Fatalf("pico2-bus spi_tcp = %+v", bus.SPITCP)
	}
}

func TestBoardsRejectBusConflicts(t *testing.T) {
	writeBoards(t, `
boards:
  - id: dup
    touch: { bus: 0, addr: 0x50, int: 1, width: 240, height: 320 }
    i2c_tcp:
      - { bus: 0, addrs: [0x50] }
`)
	if _, err := LoadBoards(); err == nil {
		t.Fatal("duplicate I2C address was accepted")
	}
	writeBoards(t, `
boards:
  - id: dup
    lcd: { spi: 0, cs: 17, dc: 16, rst: 20, width: 240, height: 320 }
    spi_tcp:
      - { bus: 0, cs: 17 }
`)
	if _, err := LoadBoards(); err == nil {
		t.Fatal("duplicate SPI CS was accepted")
	}
}

// TestBoardsValidateInputs checks the interactive-control schema: unique ids,
// valid package pins, ADC-only pins 26-29, distinct encoder lines, no shared
// GPIO and a single BOOTSEL.
func TestBoardsValidateInputs(t *testing.T) {
	bad := []string{
		`inputs: [{ kind: adc, id: p, pin: 20 }]`,
		`inputs: [{ kind: button, id: b, pin: 40 }]`,
		`inputs: [{ kind: button, id: b, pin: 15 }, { kind: switch, id: s, pin: 15 }]`,
		`inputs: [{ kind: encoder, id: e, pin_a: 12, pin_b: 12 }]`,
		`inputs: [{ kind: button, id: b, pin: 15 }, { kind: button, id: b, pin: 16 }]`,
		`inputs: [{ kind: nope, id: x, pin: 15 }]`,
		`inputs: [{ kind: bootsel, id: a }, { kind: bootsel, id: b }]`,
	}
	for _, snippet := range bad {
		writeBoards(t, "boards:\n  - id: t\n    "+snippet+"\n")
		if _, err := LoadBoards(); err == nil {
			t.Fatalf("invalid inputs accepted: %s", snippet)
		}
	}

	writeBoards(t, `boards:
  - id: ok
    package: qfn80
    inputs:
      - { kind: Button, id: b, pin: 40 }
      - { kind: slide, id: s, pin: 14, initial: true }
      - { kind: rotary, id: e, pin_a: 12, pin_b: 13 }
      - { kind: pot, id: p, pin: 27 }
      - { kind: boot, id: boot }
`)
	boards, err := LoadBoards()
	if err != nil {
		t.Fatal(err)
	}
	in := boards[0].Inputs
	if in[0].Kind != "button" || in[1].Kind != "switch" || in[2].Kind != "encoder" ||
		in[3].Kind != "adc" || in[4].Kind != "bootsel" {
		t.Fatalf("kinds not normalized: %+v", in)
	}
	if in[3].Min != 0 || in[3].Max != 3.3 || in[3].DefaultVolts() != 1.65 {
		t.Fatalf("adc defaults = %+v", in[3])
	}
	if in[0].Label != "B" {
		t.Fatalf("label default = %q", in[0].Label)
	}
	if !in[1].Initial {
		t.Fatal("switch initial position lost")
	}
}

// TestEmbeddedInputsBoard guards the pico2-inputs demo board and the BOOTSEL
// entry on the default Pico 2 board.
func TestEmbeddedInputsBoard(t *testing.T) {
	boards, err := LoadBoards()
	if err != nil {
		t.Fatal(err)
	}
	var demo, pico *BoardDef
	for i := range boards {
		switch boards[i].ID {
		case "pico2-inputs":
			demo = &boards[i]
		case "pico2":
			pico = &boards[i]
		}
	}
	if demo == nil {
		t.Fatal("pico2-inputs board is missing")
	}
	kinds := map[string]bool{}
	for _, in := range demo.Inputs {
		kinds[in.Kind] = true
	}
	for _, k := range []string{"button", "switch", "encoder", "adc", "bootsel"} {
		if !kinds[k] {
			t.Fatalf("pico2-inputs is missing a %s control: %+v", k, demo.Inputs)
		}
	}
	if pico == nil || len(pico.Inputs) != 1 || pico.Inputs[0].Kind != "bootsel" {
		t.Fatalf("pico2 bootsel input = %+v", pico)
	}
}

// TestBoardsADCAndDevicePinValidation covers the audit fixes: min-only ADC
// ranges default max to 3.3 V, and an input may not claim a device pin.
func TestBoardsADCAndDevicePinValidation(t *testing.T) {
	writeBoards(t, `boards:
  - id: adc
    inputs:
      - { kind: adc, id: p, pin: 26, min: 1.0 }
`)
	boards, err := LoadBoards()
	if err != nil {
		t.Fatal(err)
	}
	if got := boards[0].Inputs[0]; got.Min != 1.0 || got.Max != 3.3 {
		t.Fatalf("min-only ADC range = %v..%v, want 1..3.3", got.Min, got.Max)
	}
	writeBoards(t, `boards:
  - id: clash
    lcd: { spi: 0, cs: 17, dc: 16, rst: 20, width: 240, height: 135 }
    inputs:
      - { kind: button, id: b, pin: 17 }
`)
	if _, err := LoadBoards(); err == nil {
		t.Fatal("input on the LCD chip select was accepted")
	}
}

// TestEmbeddedPresetBoard guards the built-in bus preset board.
func TestEmbeddedPresetBoard(t *testing.T) {
	boards, err := LoadBoards()
	if err != nil {
		t.Fatal(err)
	}
	var b *BoardDef
	for i := range boards {
		if boards[i].ID == "pico2-presets" {
			b = &boards[i]
		}
	}
	if b == nil {
		t.Fatal("pico2-presets board is missing")
	}
	if len(b.I2CPresets) != 3 || len(b.SPIPresets) != 1 {
		t.Fatalf("presets = %+v / %+v", b.I2CPresets, b.SPIPresets)
	}
	if b.I2CPresets[0].Preset != "bme280" || b.I2CPresets[0].Addr != 0x76 {
		t.Fatalf("bme280 = %+v", b.I2CPresets[0])
	}
	if b.SPIPresets[0].Preset != "w25q16" || b.SPIPresets[0].CS != 17 {
		t.Fatalf("w25q16 = %+v", b.SPIPresets[0])
	}
}

func TestBoardsValidatePresets(t *testing.T) {
	good := `boards:
  - id: ok
    i2c_presets:
      - { bus: 0, preset: bme280 }
      - { bus: 1, preset: ssd1306, addr: 0x3d }
    spi_presets:
      - { bus: 0, cs: 17, preset: w25q16 }
`
	writeBoards(t, good)
	boards, err := LoadBoards()
	if err != nil {
		t.Fatal(err)
	}
	if got := boards[0].I2CPresets[0].Addr; got != 0x76 {
		t.Fatalf("default address = %#x, want 0x76", got)
	}
	bad := []string{
		`i2c_presets: [{ bus: 0, preset: nope }]`,
		`i2c_presets: [{ bus: 0, preset: w25q16 }]`,
		`i2c_presets: [{ bus: 2, preset: bme280 }]`,
		`i2c_presets: [{ bus: 0, preset: bme280, addr: 0x50 }]`,
		`spi_presets: [{ bus: 0, cs: 17, preset: bme280 }]`,
		`spi_presets: [{ bus: 0, cs: 99, preset: w25q16 }]`,
		`touch: { bus: 0, addr: 0x76, int: 1, width: 240, height: 320 }
		 i2c_presets: [{ bus: 0, preset: bme280, addr: 0x76 }]`,
		`lcd: { spi: 0, cs: 17, dc: 16, rst: 20, width: 240, height: 320 }
		 spi_presets: [{ bus: 0, cs: 17, preset: w25q16 }]`,
	}
	for _, snippet := range bad {
		writeBoards(t, "boards:\n  - id: t\n    "+snippet+"\n")
		if _, err := LoadBoards(); err == nil {
			t.Fatalf("invalid preset config accepted: %s", snippet)
		}
	}
}
