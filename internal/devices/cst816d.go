package devices

import "github.com/akhenakh/rp2350sim/internal/rp2350"

// CST816D register map. The chip exposes the touch point starting at the
// finger-count register; a burst read of 0x02..0x06 is the common poll path.
const (
	cstGestureID = 0x01
	cstFingerNum = 0x02
	cstXposH     = 0x03
	cstXposL     = 0x04
	cstYposH     = 0x05
	cstYposL     = 0x06
	cstChipID    = 0xA7
	cstProjID    = 0xA8
	cstFwVersion = 0xA9
	cstMotion    = 0xAA
	cstIrqCtl    = 0xFA
	cstDisSleep  = 0xFE
)

// CST816D models the capacitive touch controller on the RP2350-Touch-LCD-2.
//
// The panel is polled over I2C0 at 0x15. A touch is reported in the finger
// count and X/Y registers; the INT line pulses high while the point is
// unread, and reading any of the point registers clears it, matching the
// chip's report-on-read behaviour.
type CST816D struct {
	Chip *rp2350.Chip

	Addr        uint8
	INT, RST    int
	Width       int
	Height      int
	TouchRotate int // panel rotation applied by the host driver

	regs [256]byte

	readPtr   byte
	writeBase bool // the next write is a register pointer, not data
	touchRead bool

	x, y    int
	pressed bool
	gesture byte

	// A touch injected while the shared reset line is asserted is replayed
	// when the controller leaves reset.
	wantValid    bool
	wantX, wantY int
	wantPressed  bool
}

// NewCST816D creates a touch controller wired to the given reset and interrupt
// pins. Use -1 for pins that are not connected.
func NewCST816D(chip *rp2350.Chip, addr uint8, rst, intPin int) *CST816D {
	d := &CST816D{
		Chip:        chip,
		Addr:        addr,
		RST:         rst,
		INT:         intPin,
		Width:       240,
		Height:      320,
		TouchRotate: 0,
	}
	d.reset()
	return d
}

func (d *CST816D) reset() {
	d.regs = [256]byte{}
	d.regs[cstChipID] = 0xB6 // CST816D
	d.regs[cstProjID] = 0x00
	d.regs[cstFwVersion] = 0x01
	d.regs[cstIrqCtl] = 0x00
	d.readPtr = 0
	d.writeBase = true
	d.touchRead = false
	d.x, d.y = 0, 0
	d.pressed = false
	d.gesture = 0
	d.driveINT(false)
}

// inReset reports whether the shared reset line is holding the chip down.
func (d *CST816D) inReset() bool {
	return resetAsserted(d.Chip, d.RST)
}

// SetTouch injects a touch point in panel coordinates. A press or a move
// raises INT until the host reads the point; a release clears the registers.
func (d *CST816D) SetTouch(x, y int, pressed bool) {
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if x >= d.Width {
		x = d.Width - 1
	}
	if y >= d.Height {
		y = d.Height - 1
	}
	d.wantValid = true
	d.wantX, d.wantY, d.wantPressed = x, y, pressed
	if d.inReset() {
		return
	}
	changed := pressed != d.pressed || x != d.x || y != d.y
	d.x, d.y = x, y
	if !pressed {
		if d.pressed {
			d.pressed = false
			d.writePoint()
			d.driveINT(false)
		}
		return
	}
	d.pressed = true
	if changed {
		d.writePoint()
		d.driveINT(true)
	}
}

// SetGesture records a gesture identifier (0 means none) for the next read.
func (d *CST816D) SetGesture(g byte) {
	if d.inReset() {
		return
	}
	d.gesture = g
	d.regs[cstGestureID] = g
	d.driveINT(true)
}

// Last returns the current injected point and whether a finger is down.
func (d *CST816D) Last() (x, y int, pressed bool) {
	return d.x, d.y, d.pressed
}

// Gesture returns the gesture identifier reported by the last read.
func (d *CST816D) Gesture() byte { return d.gesture }

// writePoint refreshes the point registers without touching INT.
func (d *CST816D) writePoint() {
	if d.pressed {
		d.regs[cstFingerNum] = 1
		d.regs[cstXposH] = byte(d.x>>8) & 0x0F
		d.regs[cstXposL] = byte(d.x)
		d.regs[cstYposH] = byte(d.y>>8) & 0x0F
		d.regs[cstYposL] = byte(d.y)
		return
	}
	d.regs[cstFingerNum] = 0
	d.regs[cstXposH] = 0
	d.regs[cstXposL] = 0
	d.regs[cstYposH] = 0
	d.regs[cstYposL] = 0
}

func (d *CST816D) driveINT(high bool) {
	if d.INT < 0 {
		return
	}
	bit := uint64(1) << uint(d.INT)
	d.Chip.Pins.ExtValid |= bit
	if high {
		d.Chip.Pins.ExtLevel |= bit
	} else {
		d.Chip.Pins.ExtLevel &^= bit
	}
}

// BeginTransaction starts a new I2C transfer. The register pointer is kept
// because a write of the pointer is followed by a repeated-start read.
func (d *CST816D) BeginTransaction() {
	if d.inReset() {
		d.reset()
	} else if d.wantValid {
		d.applyWant()
	}
	d.writeBase = true
	d.touchRead = false
}

// applyWant replays a touch that arrived while reset was asserted.
func (d *CST816D) applyWant() {
	if !d.wantValid {
		return
	}
	d.wantValid = false
	d.x, d.y = d.wantX, d.wantY
	d.pressed = d.wantPressed
	d.writePoint()
	if d.pressed {
		d.driveINT(true)
	} else {
		d.driveINT(false)
	}
}

// EndTransaction completes the transfer. Reading the point consumes the INT
// pulse; the finger stays down and keeps reporting until the host sees a
// release through SetTouch.
func (d *CST816D) EndTransaction() {
	if d.touchRead {
		d.touchRead = false
		d.driveINT(false)
	}
	d.writeBase = true
}

// Write receives the register pointer followed by data bytes.
func (d *CST816D) Write(b byte) {
	if d.inReset() {
		d.reset()
		return
	}
	if d.writeBase {
		d.readPtr = b
		d.writeBase = false
		return
	}
	d.regs[d.readPtr] = b
	d.readPtr++
}

// Read returns the pointed register and advances the pointer. Reading any part
// of the point marks the report consumed.
func (d *CST816D) Read() byte {
	if d.inReset() {
		d.reset()
		return 0
	}
	if d.readPtr >= cstFingerNum && d.readPtr <= cstYposL {
		d.touchRead = true
	}
	v := d.regs[d.readPtr]
	d.readPtr++
	return v
}

// Tick replays a pending injection once the shared reset line is released.
// Firmware often resets the panel after the host has already queued a touch.
func (d *CST816D) Tick() {
	if d.wantValid && !d.inReset() {
		d.applyWant()
	}
}
