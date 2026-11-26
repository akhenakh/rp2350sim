// Package devices models external chips. Wire provides the timing and
// edge-level plumbing for devices that live on a single GPIO, such as 1-Wire,
// DHT and ultrasonic sensors.
package devices

import "github.com/akhenakh/rp2350sim/internal/rp2350"

// Wire is one GPIO used as a bidirectional timing line. It is a thin layer
// over the chip's pin state: devices drive the line through Wire, observe host
// edges through OnEdge, and schedule transitions on the virtual clock with
// After. All callbacks run on the emulator thread.
type Wire struct {
	chip      *rp2350.Chip
	pin       int
	listeners []func(level bool)
	suppress  int
}

// NewWire binds a wire to a GPIO pin.
func NewWire(c *rp2350.Chip, pin int) *Wire {
	w := &Wire{chip: c, pin: pin}
	c.Pins.AddChangeListener(w.edge)
	return w
}

// Pin returns the GPIO number.
func (w *Wire) Pin() int { return w.pin }

// Level returns the resolved wire level.
func (w *Wire) Level() bool { return w.chip.Pins.Level(w.pin) }

// DriveLow pulls the line low.
func (w *Wire) DriveLow() { w.drive(false) }

// DriveHigh drives the line high (push-pull devices only).
func (w *Wire) DriveHigh() { w.drive(true) }

// Release stops driving the line; the pull-up (if enabled) takes over.
func (w *Wire) Release() {
	w.suppress++
	w.chip.Pins.ReleaseExternal(w.pin)
	w.suppress--
}

func (w *Wire) drive(level bool) {
	w.suppress++
	w.chip.Pins.SetExternal(w.pin, level)
	w.suppress--
}

// OnEdge registers an observer for host transitions. Edges caused by the
// device itself are suppressed, so a state machine only sees the master.
func (w *Wire) OnEdge(fn func(level bool)) {
	if fn != nil {
		w.listeners = append(w.listeners, fn)
	}
}

func (w *Wire) edge(pin int, level bool) {
	if pin != w.pin || w.suppress > 0 {
		return
	}
	for _, fn := range w.listeners {
		fn(level)
	}
}

// After schedules fn after us virtual microseconds. The returned deadline can
// be cancelled by the device.
func (w *Wire) After(us uint64, fn func()) *rp2350.Deadline {
	d := w.chip.AddDeadline(fn)
	d.Schedule(w.chip.Cycles + us*uint64(rp2350.CyclesPerMicrosecond))
	return d
}

// Now returns the current virtual cycle.
func (w *Wire) Now() uint64 { return w.chip.Cycles }
