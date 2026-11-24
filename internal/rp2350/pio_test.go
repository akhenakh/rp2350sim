package rp2350

import "testing"

// ws2812Program is the piolib WS2812B program (shift left, autopull at 24
// bits), used here to exercise a real PIO program end to end.
var ws2812Program = [8]uint16{
	0x80E0, // pull ifempty block
	0xE001, // set pins, 1
	0x6041, // out y, 1
	0x0065, // jmp !y, 5
	0x0206, // jmp 6 [2]
	0xE200, // set pins, 0 [2]
	0xE000, // set pins, 0
	0x01E1, // jmp !osre, 1 [1]
}

func (c *Chip) pioWrite(off uint32, v uint32) { c.Write32(pioBase0+off, v) }

// configureWS2812 sets up PIO0 state machine 0 to drive GP0 with the program
// above, mirroring what piolib.NewWS2812B does.
func configureWS2812(c *Chip, clkdivWhole uint32) {
	configurePads(c)
	for i, instr := range ws2812Program {
		c.pioWrite(0x48+uint32(i)*4, uint32(instr))
	}
	// SET pins base 0, count 1; rest zero.
	c.pioWrite(0xDC, 1<<26)
	// wrap top 7, bottom 0.
	c.pioWrite(0xCC, 7<<12)
	// FJOIN_TX, shift left, autopull, threshold 24.
	c.pioWrite(0xD0, 1<<30|1<<17|24<<25)
	c.pioWrite(0xC8, clkdivWhole<<16)
	c.Pins.Func[0] = pioFuncSel(0)

	// SET PINDIRS 1 (sets GP0 as an output), then jump to 0.
	c.pioWrite(0xD8, 0xE081) // set pindirs, 1
	c.pioWrite(0xD8, 0x0000) // jmp 0
	c.pioWrite(0x000, 1)     // enable SM0
}

// ws2812Pulses runs the machine until the TX FIFO drains and returns the high
// pulse widths in CPU cycles, in order.
func ws2812Pulses(t *testing.T, c *Chip, word uint32, limit uint64) []int {
	c.pioWrite(0x010, word) // TXF0
	var widths []int
	level := c.Pins.Level(0)
	last := uint64(0)
	for range limit {
		c.Step()
		nl := c.Pins.Level(0)
		if nl != level {
			if level && nl == false {
				widths = append(widths, int(c.Cycles-last))
			}
			if !level && nl == true {
				last = c.Cycles
			}
			level = nl
		}
		if len(widths) > 64 {
			break
		}
	}
	return widths
}

func TestPIOWS2812PulseWidths(t *testing.T) {
	c := New()
	configureWS2812(c, 20)
	pulses := ws2812Pulses(t, c, 0x00FF0000, 200000) // G=0x00, R=0xFF, B=0x00
	if len(pulses) != 24 {
		t.Fatalf("got %d pulses, want 24: %v", len(pulses), pulses)
	}
	// Classify each pulse as short (0) or long (1) by comparing to the
	// midpoint between the two widths in this run.
	min, max := pulses[0], pulses[0]
	for _, w := range pulses {
		if w < min {
			min = w
		}
		if w > max {
			max = w
		}
	}
	mid := (min + max) / 2
	var bits []byte
	for _, w := range pulses {
		if w > mid {
			bits = append(bits, 1)
		} else {
			bits = append(bits, 0)
		}
	}
	want := []byte{
		0, 0, 0, 0, 0, 0, 0, 0, // G = 0x00
		1, 1, 1, 1, 1, 1, 1, 1, // R = 0xFF
		0, 0, 0, 0, 0, 0, 0, 0, // B = 0x00
	}
	for i := range want {
		if bits[i] != want[i] {
			t.Fatalf("bit %d = %d, want %d (widths %v)", i, bits[i], want[i], pulses)
		}
	}
	// A 1.25us bit at a divide-by-20 PIO clock: the long pulse should be about
	// twice the short one.
	if max < min*3/2 || max > min*3 {
		t.Fatalf("pulse widths not distinguishable: min=%d max=%d all=%v", min, max, pulses)
	}
}

// TestPIORestartBitsSelfClear checks the write-only restart strobes, which
// the SDK sets without clearing; leaving them latched re-restarts the machine
// on a later read-modify-write and loses the program entry point.
func TestPIORestartBitsSelfClear(t *testing.T) {
	c := New()
	c.Write32(pioBase0, 1<<4) // SM0 restart
	if got := c.Read32(pioBase0); got&(1<<4) != 0 {
		t.Fatalf("restart bit latched: %#x", got)
	}
	c.pio[0].sm[0].pc = 24
	c.Write32(pioBase0+0x2000, 1) // SET enable SM0
	if pc := c.pio[0].sm[0].pc; pc != 24 {
		t.Fatalf("enable restarted SM0 to wrap target %d, want 24", pc)
	}
}

// TestPIOGPIOBase checks PIO_GPIOBASE (0x168): with a base of 16, PIO pin 4
// drives system GPIO 20, which is how RP2350B programs reach GP32-47.
func TestPIOGPIOBase(t *testing.T) {
	c := New()
	c.Write32(pioBase0+0x168, 0x10)
	c.Pins.Func[20] = pioFuncSel(0)
	p := c.pio[0]
	p.sm[0].writePinRange(4, 1, 1, false)
	if c.Pins.Out&(1<<20) == 0 {
		t.Fatal("GPIOBASE=16: PIO pin 4 did not drive GP20")
	}
	if c.Pins.Out&(1<<4) != 0 {
		t.Fatal("GPIOBASE=16: PIO pin 4 drove GP4")
	}
	p.sm[0].writePinRange(4, 1, 0, false)
	if c.Pins.Out&(1<<20) != 0 {
		t.Fatal("GPIOBASE=16: GP20 did not clear")
	}
}
