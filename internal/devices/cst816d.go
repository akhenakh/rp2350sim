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
	resetting bool

	x, y           int
	pressed        bool
	touchPending   bool
	gesture        byte
	gesturePending bool

	// Inputs injected while the shared reset line is asserted are replayed
	// independently when the controller leaves reset.
	wantValid        bool
	wantX, wantY     int
	wantPressed      bool
	wantGesture      byte
	wantGestureValid bool
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
	d.touchPending = false
	d.gesture = 0
	d.gesturePending = false
	d.driveINT(false)
}

// inReset reports whether the shared reset line is holding the chip down.
func (d *CST816D) inReset() bool {
	return resetAsserted(d.Chip, d.RST)
}

// syncReset applies reset-line transitions even when no I2C access is active.
// Pending host input survives reset and is replayed on the release edge.
func (d *CST816D) syncReset() bool {
	if d.inReset() {
		if !d.resetting {
			d.resetting = true
			d.reset()
		}
		d.driveINT(false)
		return true
	}
	if d.resetting {
		d.resetting = false
		d.applyPending()
	}
	return false
}

func (d *CST816D) updateINT() {
	d.driveINT(d.touchPending || d.gesturePending)
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
	if d.syncReset() {
		d.wantValid = true
		d.wantX, d.wantY, d.wantPressed = x, y, pressed
		return
	}
	changed := pressed != d.pressed || x != d.x || y != d.y
	d.x, d.y = x, y
	if !pressed {
		if d.pressed {
			d.pressed = false
			d.touchPending = false
			d.writePoint()
			d.updateINT()
		}
		return
	}
	d.pressed = true
	if changed {
		d.touchPending = true
		d.writePoint()
		d.updateINT()
	}
}

// SetGesture records a gesture identifier (0 means none) for the next read.
func (d *CST816D) SetGesture(g byte) {
	if d.syncReset() {
		d.wantGesture = g
		d.wantGestureValid = true
		return
	}
	d.gesture = g
	d.regs[cstGestureID] = g
	d.gesturePending = g != 0
	d.updateINT()
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
	d.syncReset()
	d.writeBase = true
	d.touchRead = false
}

// applyPending replays input that arrived while reset was asserted.
func (d *CST816D) applyPending() {
	if d.wantValid {
		d.wantValid = false
		d.x, d.y = d.wantX, d.wantY
		d.pressed = d.wantPressed
		d.touchPending = d.pressed
		d.writePoint()
	}
	if d.wantGestureValid {
		d.wantGestureValid = false
		d.gesture = d.wantGesture
		d.regs[cstGestureID] = d.gesture
		d.gesturePending = d.gesture != 0
	}
	d.updateINT()
}

// EndTransaction completes the transfer. Reading the point consumes the INT
// pulse; the finger stays down and keeps reporting until the host sees a
// release through SetTouch.
func (d *CST816D) EndTransaction() {
	if d.touchRead {
		d.touchRead = false
		d.touchPending = false
		d.updateINT()
	}
	d.writeBase = true
}

// Write receives the register pointer followed by data bytes.
func (d *CST816D) Write(b byte) {
	if d.syncReset() {
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
// of the point marks the touch report consumed; gesture reports are consumed
// independently by reading the gesture register.
func (d *CST816D) Read() byte {
	if d.syncReset() {
		return 0
	}
	if d.readPtr >= cstFingerNum && d.readPtr <= cstYposL {
		d.touchRead = true
	}
	v := d.regs[d.readPtr]
	if d.readPtr == cstGestureID && d.gesturePending {
		d.gesturePending = false
		d.gesture = 0
		d.regs[cstGestureID] = 0
		d.updateINT()
	}
	d.readPtr++
	return v
}

// Tick applies reset-line transitions every cycle and replays pending input
// once the shared reset line is released.
func (d *CST816D) Tick() {
	if d.syncReset() {
		d.driveINT(false)
		return
	}
	if d.wantValid || d.wantGestureValid {
		d.applyPending()
	}
}
