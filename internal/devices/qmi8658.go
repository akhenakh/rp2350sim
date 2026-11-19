package devices

import (
	"math"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// QMI8658 register map (QMI8658A/C). Configuration registers are at 0x02..,
// the output block starts at 0x33 with temperature and runs through 0x40.
const (
	qmiWHOAMI   = 0x00
	qmiRevision = 0x01
	qmiCTRL1    = 0x02
	qmiCTRL2    = 0x03
	qmiCTRL3    = 0x04
	qmiCTRL5    = 0x06
	qmiCTRL7    = 0x08
	qmiSTATUS1  = 0x2D
	qmiSTATUS0  = 0x2E
	qmiTempL    = 0x33
	qmiAccelX   = 0x35
	qmiGyroX    = 0x3B
	qmiReset    = 0x60
)

// QMI8658 models the 6-axis IMU. The accelerometer reports a fixed gravity
// vector (plus any injected motion) and the gyroscope reports injected rates,
// scaled exactly like the chip would for the configured full-scale range.
type QMI8658 struct {
	Chip *rp2350.Chip

	Addr     uint8
	INT1     int
	INT2     int
	Interval uint64 // cycles between samples

	regs [256]byte

	accel [3]float32 // g
	gyro  [3]float32 // deg/s

	readPtr   byte
	writeBase bool

	ready    bool
	lastTick uint64
}

// NewQMI8658 creates the IMU with gravity on +Z and a zero rate.
func NewQMI8658(chip *rp2350.Chip, addr uint8, int1, int2 int) *QMI8658 {
	m := &QMI8658{
		Chip:     chip,
		Addr:     addr,
		INT1:     int1,
		INT2:     int2,
		Interval: 1_500_000, // 10 ms at the 150 MHz virtual clock
		accel:    [3]float32{0, 0, 1},
	}
	m.reset()
	return m
}

func (m *QMI8658) reset() {
	m.regs = [256]byte{}
	m.regs[qmiWHOAMI] = 0x05
	m.regs[qmiRevision] = 0x7D
	m.regs[qmiCTRL1] = 0x40 // address auto-increment
	m.regs[qmiCTRL2] = 0x23 // 8 g, 1 kHz
	m.regs[qmiCTRL3] = 0x53 // 512 dps, 1 kHz
	m.regs[qmiReset] = 0x80
	m.readPtr = 0
	m.writeBase = true
	m.ready = false
	m.driveINT(false)
}

// SetAccel injects an acceleration in g.
func (m *QMI8658) SetAccel(x, y, z float32) { m.accel = [3]float32{x, y, z} }

// SetGyro injects an angular rate in degrees per second.
func (m *QMI8658) SetGyro(x, y, z float32) { m.gyro = [3]float32{x, y, z} }

// Accel returns the current acceleration in g.
func (m *QMI8658) Accel() (x, y, z float32) { return m.accel[0], m.accel[1], m.accel[2] }

// Gyro returns the current angular rate in deg/s.
func (m *QMI8658) Gyro() (x, y, z float32) { return m.gyro[0], m.gyro[1], m.gyro[2] }

// Temperature returns the die temperature the chip reports (about 25 C).
func (m *QMI8658) Temperature() float32 { return 25 }

// Enabled reports whether the accelerometer and gyroscope are running.
func (m *QMI8658) Enabled() (accel, gyro bool) {
	return m.regs[qmiCTRL7]&1 != 0, m.regs[qmiCTRL7]&2 != 0
}

// Tick advances the data-ready state. It is a cycle hook, so samples are
// deterministic in virtual time.
func (m *QMI8658) Tick() {
	now := m.Chip.Cycles
	if now < m.lastTick+m.Interval {
		return
	}
	m.lastTick = now
	accel, gyro := m.Enabled()
	if !accel && !gyro {
		return
	}
	m.ready = true
	m.driveINT(true)
}

func (m *QMI8658) driveINT(high bool) {
	for _, pin := range []int{m.INT1, m.INT2} {
		if pin < 0 {
			continue
		}
		bit := uint64(1) << uint(pin)
		m.Chip.Pins.ExtValid |= bit
		if high {
			m.Chip.Pins.ExtLevel |= bit
		} else {
			m.Chip.Pins.ExtLevel &^= bit
		}
	}
}

// accelLSB is the raw counts per g for the range in CTRL2.
func (m *QMI8658) accelLSB() float32 {
	return float32(int(16384) >> ((m.regs[qmiCTRL2] >> 4) & 3))
}

// gyroLSB is the raw counts per deg/s for the range in CTRL3.
func (m *QMI8658) gyroLSB() float32 {
	return float32(int(2048) >> ((m.regs[qmiCTRL3] >> 4) & 7))
}

func (m *QMI8658) status() byte {
	accel, gyro := m.Enabled()
	var v byte
	if m.ready {
		if accel {
			v |= 1
		}
		if gyro {
			v |= 2
		}
	}
	return v
}

func (m *QMI8658) accelRaw(axis int) int16 {
	v := math.Round(float64(m.accel[axis] * m.accelLSB()))
	return clampInt16(v)
}

func (m *QMI8658) gyroRaw(axis int) int16 {
	v := math.Round(float64(m.gyro[axis] * m.gyroLSB()))
	return clampInt16(v)
}

func clampInt16(v float64) int16 {
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return int16(v)
}

func (m *QMI8658) dataByte(reg byte) byte {
	switch {
	case reg == qmiSTATUS1:
		return 0
	case reg == qmiSTATUS0:
		v := m.status()
		m.clearReady()
		return v
	case reg == qmiTempL:
		t := int16(math.Round(float64(m.Temperature()) * 256))
		return byte(t)
	case reg == qmiTempL+1:
		t := int16(math.Round(float64(m.Temperature()) * 256))
		return byte(uint16(t) >> 8)
	case reg >= qmiAccelX && reg < qmiAccelX+6:
		i := int(reg - qmiAccelX)
		v := uint16(m.accelRaw(i / 2))
		m.clearReady()
		if i%2 == 0 {
			return byte(v)
		}
		return byte(v >> 8)
	case reg >= qmiGyroX && reg < qmiGyroX+6:
		i := int(reg - qmiGyroX)
		v := uint16(m.gyroRaw(i / 2))
		m.clearReady()
		if i%2 == 0 {
			return byte(v)
		}
		return byte(v >> 8)
	}
	return m.regs[reg]
}

// clearReady drops the data-ready flag once a result has been read.
func (m *QMI8658) clearReady() {
	if m.ready {
		m.ready = false
		m.driveINT(false)
	}
}

// BeginTransaction starts an I2C transfer.
func (m *QMI8658) BeginTransaction() { m.writeBase = true }

// EndTransaction ends an I2C transfer.
func (m *QMI8658) EndTransaction() { m.writeBase = true }

// Write receives the register pointer followed by configuration data.
func (m *QMI8658) Write(b byte) {
	if m.writeBase {
		m.readPtr = b
		m.writeBase = false
		return
	}
	if m.readPtr == qmiReset && b == 0xB0 {
		accel, gyro := m.accel, m.gyro
		m.reset()
		m.accel, m.gyro = accel, gyro
		return
	}
	m.regs[m.readPtr] = b
	m.readPtr++
}

// Read returns the pointed register and advances the pointer.
func (m *QMI8658) Read() byte {
	v := m.dataByte(m.readPtr)
	m.readPtr++
	return v
}
