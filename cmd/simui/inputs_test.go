package main

import (
	"math"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// testInputsBoard is a board with one of every interactive control on the
// pins used by the embedded pico2-inputs definition.
func testInputsBoard() *BoardDef {
	minV, maxV := 0.0, 3.3
	return &BoardDef{
		ID: "test-inputs",
		Inputs: []InputDef{
			{Kind: "button", ID: "btn", Pin: 15, ActiveLow: new(true)},
			{Kind: "switch", ID: "sw", Pin: 14, ActiveLow: new(true)},
			{Kind: "encoder", ID: "enc", PinA: 12, PinB: 13, ActiveLow: new(true)},
			{Kind: "adc", ID: "pot", Pin: 26, Min: minV, Max: maxV},
			{Kind: "bootsel", ID: "boot"},
		},
	}
}

func level(t *testing.T, e *Emu, pin int) bool {
	t.Helper()
	s := e.Snapshot()
	return s.Pins[pin].Level
}

func voltage(t *testing.T, e *Emu, pin int) float32 {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.chip.Pins.Voltage(pin)
}

func qspiCSN(t *testing.T, e *Emu) uint32 {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.chip.Read32(rp2350.SIOBase+0x08) & (1 << 27)
}

// TestBoardInputsDriveChip checks that the host controls change the emulated
// pin/analog/QSPI state the firmware sees.
func TestBoardInputsDriveChip(t *testing.T) {
	e := NewEmu()
	e.SetBoard(testInputsBoard())

	// Active-low controls idle high.
	if !level(t, e, 15) {
		t.Fatal("idle button pin is low")
	}
	e.SetInput("btn", InputValue{On: true})
	if level(t, e, 15) {
		t.Fatal("pressed button pin is high")
	}
	e.SetInput("btn", InputValue{On: false})
	if !level(t, e, 15) {
		t.Fatal("released button pin is low")
	}

	e.SetInput("sw", InputValue{On: true})
	if level(t, e, 14) {
		t.Fatal("closed switch pin is high")
	}
	e.SetInput("sw", InputValue{On: false})
	if !level(t, e, 14) {
		t.Fatal("open switch pin is low")
	}

	// Phase 0 is the detent (both high after the active-low inversion).
	if !level(t, e, 12) || !level(t, e, 13) {
		t.Fatal("encoder detent is not idle high")
	}
	e.StepEncoder("enc", 1)
	if level(t, e, 12) || !level(t, e, 13) {
		t.Fatal("encoder phase 1 levels are wrong")
	}
	e.StepEncoder("enc", 1)
	if level(t, e, 12) || level(t, e, 13) {
		t.Fatal("encoder phase 2 levels are wrong")
	}
	e.StepEncoder("enc", 1)
	if !level(t, e, 12) || level(t, e, 13) {
		t.Fatal("encoder phase 3 levels are wrong")
	}
	e.StepEncoder("enc", 1)
	if !level(t, e, 12) || !level(t, e, 13) {
		t.Fatal("encoder did not wrap to the detent")
	}
	// A counter-clockwise step wraps to phase 3.
	e.StepEncoder("enc", -1)
	if !level(t, e, 12) || level(t, e, 13) {
		t.Fatal("encoder reverse step levels are wrong")
	}

	e.SetInput("pot", InputValue{Volts: 2.5})
	if got := voltage(t, e, 26); math.Abs(float64(got)-2.5) > 0.01 {
		t.Fatalf("pot voltage = %v, want 2.5", got)
	}
	// Out-of-range values are clamped to the input range.
	e.SetInput("pot", InputValue{Volts: 9})
	if got := voltage(t, e, 26); math.Abs(float64(got)-3.3) > 0.01 {
		t.Fatalf("clamped pot voltage = %v, want 3.3", got)
	}

	if qspiCSN(t, e) == 0 {
		t.Fatal("QSPI_CSN idle is low")
	}
	e.SetBootsel(true)
	if qspiCSN(t, e) != 0 {
		t.Fatal("QSPI_CSN did not go low while BOOTSEL is held")
	}
	if s := e.Snapshot(); !s.BootselHeld {
		t.Fatal("snapshot does not report BOOTSEL held")
	}
	// The bootsel input and the diagram share one state.
	e.SetInput("boot", InputValue{On: false})
	if qspiCSN(t, e) == 0 {
		t.Fatal("releasing the bootsel input left QSPI_CSN low")
	}
}

// TestBoardInputsSurviveReset checks that switch, pot and BOOTSEL positions are
// re-applied after a chip reset (which clears the pin model).
func TestBoardInputsSurviveReset(t *testing.T) {
	e := NewEmu()
	e.SetBoard(testInputsBoard())
	e.SetInput("sw", InputValue{On: true})
	e.SetInput("pot", InputValue{Volts: 1.2})
	e.SetBootsel(true)
	e.Reset()
	if level(t, e, 14) {
		t.Fatal("switch position lost across reset")
	}
	if got := voltage(t, e, 26); math.Abs(float64(got)-1.2) > 0.01 {
		t.Fatalf("pot position lost across reset: %v", got)
	}
	if qspiCSN(t, e) != 0 {
		t.Fatal("BOOTSEL state lost across reset")
	}
}

func TestQuadraturePhase(t *testing.T) {
	want := [4][2]bool{{false, false}, {true, false}, {true, true}, {false, true}}
	for phase := range 4 {
		a, b := quadraturePhase(uint8(phase))
		if a != want[phase][0] || b != want[phase][1] {
			t.Fatalf("phase %d = %v,%v, want %v,%v", phase, a, b, want[phase][0], want[phase][1])
		}
	}
}

func TestParseOnOff(t *testing.T) {
	for _, s := range []string{"on", "1", "true", "press", "pressed", "down"} {
		if v, err := parseOnOff(s); err != nil || !v {
			t.Fatalf("parseOnOff(%q) = %v,%v", s, v, err)
		}
	}
	for _, s := range []string{"off", "0", "false", "release", "up"} {
		if v, err := parseOnOff(s); err != nil || v {
			t.Fatalf("parseOnOff(%q) = %v,%v", s, v, err)
		}
	}
	if _, err := parseOnOff("maybe"); err == nil {
		t.Fatal("parseOnOff accepted garbage")
	}
}

// TestBootselHeldAtReset checks that a latched BOOTSEL is applied before Boot,
// so the reset sample and firmware both see it.
func TestBootselHeldAtReset(t *testing.T) {
	e := NewEmu()
	e.SetBoard(testInputsBoard())
	e.SetBootsel(true)
	e.Reset()
	s := e.Snapshot()
	if !s.BootselHeld || !s.BootselAtBoot {
		t.Fatalf("bootsel held=%v atBoot=%v, want both true", s.BootselHeld, s.BootselAtBoot)
	}
	e.SetBootsel(false)
	e.Reset()
	if s := e.Snapshot(); s.BootselAtBoot {
		t.Fatal("BOOTSEL reported at a reset without it held")
	}
}

// TestQueuedEncoderSteps checks scripted encoder steps are delivered after
// boot, spaced in virtual time, instead of as one invisible phase jump.
func TestQueuedEncoderSteps(t *testing.T) {
	e := NewEmu()
	e.SetBoard(testInputsBoard())
	e.mu.Lock()
	e.chip.CPU0.Parked = true
	e.chip.CPU1.Parked = true
	e.mu.Unlock()
	e.QueueEncoder("enc", 3)
	e.RunFor(70 * 150_000) // 20 ms per step
	s := e.Snapshot()
	if got := s.Inputs["enc"].Phase; got != 3 {
		t.Fatalf("queued encoder phase = %d, want 3", got)
	}
	// The steps produced distinct quadrature phases along the way: rewind a
	// short run and check the first one landed at ~20 ms.
	e = NewEmu()
	e.SetBoard(testInputsBoard())
	e.mu.Lock()
	e.chip.CPU0.Parked = true
	e.chip.CPU1.Parked = true
	e.mu.Unlock()
	e.QueueEncoder("enc", 1)
	e.RunFor(10 * 150_000)
	if got := e.Snapshot().Inputs["enc"].Phase; got != 0 {
		t.Fatalf("encoder stepped before 20 ms: phase %d", got)
	}
	e.RunFor(10 * 150_000)
	if got := e.Snapshot().Inputs["enc"].Phase; got != 1 {
		t.Fatalf("encoder phase after 20 ms = %d, want 1", got)
	}
}
