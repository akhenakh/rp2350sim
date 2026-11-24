package devices

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func i2cWriteReg(dev interface {
	Write(byte)
	BeginTransaction()
	EndTransaction()
}, reg, val byte) {
	dev.BeginTransaction()
	dev.Write(reg)
	dev.Write(val)
	dev.EndTransaction()
}

func i2cReadReg(dev interface {
	Write(byte)
	Read() byte
	BeginTransaction()
	EndTransaction()
}, reg byte) byte {
	dev.BeginTransaction()
	dev.Write(reg)
	dev.BeginTransaction()
	v := dev.Read()
	dev.EndTransaction()
	return v
}

// TestCST816DTouch checks the identify registers, the point burst and the INT
// line behaviour.
func TestCST816DTouch(t *testing.T) {
	c := rp2350.New()
	d := NewCST816D(c, 0x15, 20, 29)
	if got := i2cReadReg(d, cstChipID); got != 0xB6 {
		t.Fatalf("chip id = %#x, want 0xB6", got)
	}

	d.SetTouch(120, 200, true)
	if !c.Pins.Level(29) {
		t.Fatal("INT is low after a press")
	}

	d.BeginTransaction()
	d.Write(cstFingerNum)
	d.BeginTransaction()
	point := make([]byte, 5)
	for i := range point {
		point[i] = d.Read()
	}
	d.EndTransaction()

	if point[0] != 1 {
		t.Fatalf("finger count = %d, want 1", point[0])
	}
	x := int(point[1]&0x0F)<<8 | int(point[2])
	y := int(point[3]&0x0F)<<8 | int(point[4])
	if x != 120 || y != 200 {
		t.Fatalf("point = %d,%d, want 120,200", x, y)
	}
	if c.Pins.Level(29) {
		t.Fatal("INT stayed high after the point was read")
	}

	d.SetTouch(120, 200, false)
	if got := i2cReadReg(d, cstFingerNum); got != 0 {
		t.Fatalf("finger count after release = %d", got)
	}
}

// TestCST816DReset checks the shared reset line clears the controller.
func TestCST816DReset(t *testing.T) {
	c := rp2350.New()
	d := NewCST816D(c, 0x15, 20, 29)
	c.Pins.OE |= 1 << 20
	c.Pins.Out &^= 1 << 20 // hold reset low
	d.SetTouch(10, 10, true)
	if c.Pins.Level(29) {
		t.Fatal("touch was accepted while held in reset")
	}
	c.Pins.Out |= 1 << 20
	i2cWriteReg(d, cstDisSleep, 1)
	if got := i2cReadReg(d, cstDisSleep); got != 1 {
		t.Fatalf("DisAutoSleep = %#x", got)
	}
}

// TestQMI8658Read checks identity, data-ready status and the scaled gravity
// vector.
func TestQMI8658Read(t *testing.T) {
	c := rp2350.New()
	m := NewQMI8658(c, 0x6B, 14, -1)
	if got := i2cReadReg(m, qmiWHOAMI); got != 0x05 {
		t.Fatalf("WHO_AM_I = %#x, want 0x05", got)
	}
	i2cWriteReg(m, qmiReset, 0xB0)
	i2cWriteReg(m, qmiCTRL2, 0x23) // 8 g
	i2cWriteReg(m, qmiCTRL7, 0x03) // accel + gyro

	if c.Pins.Level(14) {
		t.Fatal("INT is high before the first sample")
	}
	c.Cycles += m.Interval
	m.Tick()
	if !c.Pins.Level(14) {
		t.Fatal("INT is low after a sample")
	}

	m.BeginTransaction()
	m.Write(qmiAccelX)
	m.BeginTransaction()
	raw := make([]byte, 6)
	for i := range raw {
		raw[i] = m.Read()
	}
	m.EndTransaction()

	ax := int16(uint16(raw[1])<<8 | uint16(raw[0]))
	az := int16(uint16(raw[5])<<8 | uint16(raw[4]))
	if ax != 0 {
		t.Fatalf("AX = %d, want 0", ax)
	}
	if az != 4096 {
		t.Fatalf("AZ = %d, want 4096 (1 g at 8 g full scale)", az)
	}
	if c.Pins.Level(14) {
		t.Fatal("INT stayed high after data was read")
	}

	// Doubling the range halves the raw count.
	i2cWriteReg(m, qmiCTRL2, 0x13) // 4 g
	m.BeginTransaction()
	m.Write(qmiAccelX)
	m.BeginTransaction()
	for i := range raw {
		raw[i] = m.Read()
	}
	m.EndTransaction()
	az = int16(uint16(raw[5])<<8 | uint16(raw[4]))
	if az != 8192 {
		t.Fatalf("AZ at 4 g = %d, want 8192", az)
	}
}

// TestQMI8658Gyro injects a rate and checks the little-endian output.
func TestQMI8658Gyro(t *testing.T) {
	c := rp2350.New()
	m := NewQMI8658(c, 0x6B, -1, -1)
	i2cWriteReg(m, qmiCTRL3, 0x53) // 512 dps
	i2cWriteReg(m, qmiCTRL7, 0x02)
	m.SetGyro(0, 0, 100)
	m.BeginTransaction()
	m.Write(qmiGyroX)
	m.BeginTransaction()
	gz := 0
	for i := range 6 {
		b := m.Read()
		if i >= 4 {
			gz |= int(b) << (8 * (i - 4))
		}
	}
	m.EndTransaction()
	if gz != 100*64 {
		t.Fatalf("GZ = %d, want %d", gz, 100*64)
	}
}

// TestCST816DPressDuringReset checks a queued touch survives the shared reset
// line: firmware often resets the panel after the UI has queued a tap.
func TestCST816DPressDuringReset(t *testing.T) {
	c := rp2350.New()
	d := NewCST816D(c, 0x15, 20, 29)
	c.Pins.OE |= 1 << 20
	c.Pins.Out &^= 1 << 20 // reset asserted
	d.SetTouch(120, 200, true)
	c.Pins.Out |= 1 << 20 // release reset
	d.Tick()
	if !c.Pins.Level(29) {
		t.Fatal("INT is low after the press was replayed")
	}
	d.BeginTransaction()
	d.Write(cstFingerNum)
	d.BeginTransaction()
	if got := d.Read(); got != 1 {
		t.Fatalf("finger count = %d, want 1", got)
	}
}

func TestCST816DResetEveryTick(t *testing.T) {
	c := rp2350.New()
	d := NewCST816D(c, 0x15, 20, 29)
	d.SetTouch(10, 20, true)
	i2cWriteReg(d, cstDisSleep, 1)
	if !c.Pins.Level(29) {
		t.Fatal("INT is low before reset")
	}

	c.Pins.OE |= 1 << 20
	c.Pins.Out &^= 1 << 20 // assert reset without an I2C access
	d.Tick()
	if c.Pins.Level(29) {
		t.Fatal("INT stayed high while reset was held")
	}
	d.SetTouch(30, 40, true)
	c.Pins.Out |= 1 << 20 // release reset
	d.Tick()
	if !c.Pins.Level(29) {
		t.Fatal("queued touch was not replayed on release")
	}
	if got := i2cReadReg(d, cstDisSleep); got != 0 {
		t.Fatalf("reset retained DisAutoSleep=%#x", got)
	}
}

func TestCST816DGestureReportIndependent(t *testing.T) {
	c := rp2350.New()
	d := NewCST816D(c, 0x15, 20, 29)
	d.SetTouch(10, 20, true)
	d.SetGesture(0x01)

	d.BeginTransaction()
	d.Write(cstFingerNum)
	d.BeginTransaction()
	if got := d.Read(); got != 1 {
		t.Fatalf("finger count = %d", got)
	}
	d.EndTransaction()
	if !c.Pins.Level(29) {
		t.Fatal("reading the point consumed the gesture interrupt")
	}
	if got := i2cReadReg(d, cstGestureID); got != 0x01 {
		t.Fatalf("gesture = %#x, want 0x01", got)
	}
	if c.Pins.Level(29) {
		t.Fatal("INT stayed high after the gesture was read")
	}
	if got := d.Gesture(); got != 0 {
		t.Fatalf("consumed gesture = %#x, want 0", got)
	}
	if got := i2cReadReg(d, cstGestureID); got != 0 {
		t.Fatalf("gesture register was not consumed: %#x", got)
	}
}

func TestCST816DZeroGestureDoesNotAssertINT(t *testing.T) {
	c := rp2350.New()
	d := NewCST816D(c, 0x15, 20, 29)
	d.SetGesture(0)
	if c.Pins.Level(29) {
		t.Fatal("zero gesture asserted INT")
	}
}

func TestQMI8658ResetRestartsSampleInterval(t *testing.T) {
	c := rp2350.New()
	m := NewQMI8658(c, 0x6B, 14, -1)
	i2cWriteReg(m, qmiCTRL7, 0x03)
	c.Cycles = 100
	i2cWriteReg(m, qmiReset, 0xB0)
	i2cWriteReg(m, qmiCTRL7, 0x03)

	c.Cycles += m.Interval - 1
	m.Tick()
	if c.Pins.Level(14) || m.ready {
		t.Fatal("sample interval restarted before a full interval elapsed")
	}
	c.Cycles++
	m.Tick()
	if !c.Pins.Level(14) || !m.ready {
		t.Fatal("sample interval did not restart after a full interval")
	}
}

func TestQMI8658DisableClearsDataReady(t *testing.T) {
	c := rp2350.New()
	m := NewQMI8658(c, 0x6B, 14, -1)
	i2cWriteReg(m, qmiCTRL7, 0x03)
	c.Cycles = m.Interval
	m.Tick()
	if !c.Pins.Level(14) || !m.ready {
		t.Fatal("sample did not assert data ready")
	}

	i2cWriteReg(m, qmiCTRL7, 0)
	if c.Pins.Level(14) || m.ready || m.status() != 0 {
		t.Fatal("disabling both sensors left data-ready or INT asserted")
	}
}
