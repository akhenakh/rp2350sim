package devices

import "github.com/akhenakh/rp2350sim/internal/rp2350"

// HCSR04 is an ultrasonic distance sensor: a trigger pulse on one wire makes
// the echo wire stay high for distance_cm * 58 us.
type HCSR04 struct {
	trig, echo *Wire
	distanceCM float64
	action     *rp2350.Deadline
	trigHighAt uint64
	trigHigh   bool
	active     bool
	// OnMeasure is called for each accepted trigger pulse, with the decoded
	// distance and the raw echo width in microseconds.
	OnMeasure func(distanceCM float64, echoUS uint64)
}

// NewHCSR04 attaches an ultrasonic sensor to trigger and echo wires.
func NewHCSR04(trig, echo *Wire, distanceCM float64) *HCSR04 {
	if distanceCM <= 0 {
		distanceCM = 100
	}
	d := &HCSR04{trig: trig, echo: echo, distanceCM: distanceCM}
	d.action = trig.chip.AddDeadline(d.onAction)
	trig.chip.AddResetHook(d.onReset)
	echo.DriveLow()
	trig.OnEdge(func(level bool) {
		if level {
			d.trigHighAt = trig.Now()
			d.trigHigh = true
			return
		}
		if d.active || !d.trigHigh {
			return
		}
		d.trigHigh = false
		if trig.Now()-d.trigHighAt < 8*uint64(rp2350.CyclesPerMicrosecond) {
			d.trigHighAt = 0
			return
		}
		d.trigHighAt = 0
		d.active = true
		if d.OnMeasure != nil {
			d.OnMeasure(d.distanceCM, uint64(d.distanceCM*58))
		}
		d.action.Schedule(trig.Now() + 20*uint64(rp2350.CyclesPerMicrosecond))
	})
	return d
}

// SetDistance changes the measured distance.
func (d *HCSR04) SetDistance(cm float64) { d.distanceCM = cm }

func (d *HCSR04) onReset() {
	d.active = false
	d.trigHigh = false
	d.trigHighAt = 0
	d.echo.DriveLow()
}

func (d *HCSR04) onAction() {
	if !d.active {
		return
	}
	if d.echo.Level() {
		d.echo.DriveLow()
		d.active = false
		return
	}
	if d.distanceCM > 400 {
		d.active = false
		return
	}
	d.echo.DriveHigh()
	width := uint64(d.distanceCM * 58)
	if width == 0 {
		width = 1
	}
	d.action.Schedule(d.echo.Now() + width*uint64(rp2350.CyclesPerMicrosecond))
}
