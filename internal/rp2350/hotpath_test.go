package rp2350

import (
	"hash/fnv"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/m33"
)

// The hot-path caches must not change what the machine does. These tests pin
// the register-block page cache, the PIO input fast path, and the invariance of
// a run to the size of the Run chunks.

// mmioProbeProgram writes SIO GPIO_OUT and reads TIMER0 TIMERAWL in a loop, so
// the MMIO page cache, the timer tick and the instruction fetch all run.
// Encodings from llvm-mc -triple=thumbv8m.main.
var mmioProbeProgram = []byte{
	0x40, 0xf2, 0x10, 0x01, // movw r1, #0x0010
	0xcd, 0xf2, 0x00, 0x01, // movt r1, #0xd000 (SIO GPIO_OUT)
	0x40, 0xf2, 0x28, 0x00, // movw r0, #0x0028
	0xc4, 0xf2, 0x0b, 0x00, // movt r0, #0x400b (TIMER0 TIMERAWL)
	0x00, 0x22, // movs r2, #0
	0x0a, 0x60, // str r2, [r1]
	0x03, 0x68, // ldr r3, [r0]
	0x01, 0x32, // adds r2, #1
	0xfb, 0xe7, // b .-10
}

// hashMachine folds the observable machine state into one value.
func hashMachine(c *Chip) uint64 {
	h := fnv.New64a()
	put := func(v uint64) {
		var b [8]byte
		for i := range b {
			b[i] = byte(v >> (8 * i))
		}
		h.Write(b[:])
	}
	h.Write(c.SRAM)
	for _, cpu := range []*m33.CPU{c.CPU0, c.CPU1} {
		for _, v := range cpu.R {
			put(uint64(v))
		}
		for _, v := range []uint32{cpu.APSR, cpu.IPSR, cpu.PRIMASK, cpu.FAULTMASK, cpu.BASEPRI, cpu.CONTROL, cpu.MSP, cpu.PSP} {
			put(uint64(v))
		}
		if cpu.Sleeping {
			put(1)
		}
		if cpu.Parked {
			put(2)
		}
	}
	for pin := range 48 {
		put(uint64(c.Pins.Func[pin]))
		if c.Pins.Level(pin) {
			put(1)
		}
		if c.Pins.OE&(1<<uint(pin)) != 0 {
			put(2)
		}
	}
	return h.Sum64()
}

// TestMMIORegisterBlockCache interleaves accesses to different 4 KB register
// pages so the one-entry block cache is forced to re-resolve on every access,
// and checks that each write still lands in its own block.
func TestMMIORegisterBlockCache(t *testing.T) {
	const (
		spi0   = 0x40080000
		timer0 = 0x400b0000
		timer1 = 0x400b8000
		sio    = 0xd0000000
	)
	c := New()

	c.Write32(spi0+0x00, 0x00070000) // SSPCR0
	c.Write32(timer0+0x10, 0x1111)   // ALARM0
	c.Write32(timer1+0x10, 0x2222)   // ALARM0
	c.Write32(sio+0x10, 0x0000_0005)
	c.Write32(timer0+0x10, 0x3333) // back to a page used earlier
	c.Write32(timer1+0x10, 0x4444)

	checks := []struct {
		name string
		addr uint32
		want uint32
	}{
		{"SPI0 SSPCR0", spi0 + 0x00, 0x00070000},
		{"TIMER0 ALARM0", timer0 + 0x10, 0x3333},
		{"TIMER1 ALARM0", timer1 + 0x10, 0x4444},
		{"SIO GPIO_OUT", sio + 0x10, 0x5},
	}
	for _, ch := range checks {
		if got := c.Read32(ch.addr); got != ch.want {
			t.Fatalf("%s at %#x = %#x, want %#x", ch.name, ch.addr, got, ch.want)
		}
	}

	// The atomic aliases must resolve through the same path: SET at +0x2000.
	c.Write32(timer0+0x2010, 0x00f0)
	if got := c.Read32(timer0 + 0x10); got != 0x3333|0x00f0 {
		t.Fatalf("TIMER0 ALARM0 after SET alias = %#x", got)
	}
	// A different block after an alias write must still be reachable.
	c.Write32(timer1+0x3010, 0x0001) // CLR alias on TIMER1
	if got := c.Read32(timer1 + 0x10); got != 0x4444&^0x0001 {
		t.Fatalf("TIMER1 ALARM0 after CLR alias = %#x", got)
	}
}

// TestPIOInputLevelsFastPath mutates every summary the fast path compares and
// checks that the cached levels follow, so a missing comparison shows up here.
func TestPIOInputLevelsFastPath(t *testing.T) {
	c := New()
	p := c.Pins
	p.padIE = ^uint64(0)
	p.padISO = 0
	p.padOD = 0
	p.pullUpMask = 0
	p.ExtValid = 0
	p.ExtLevel = 0
	p.Out = 1
	p.OE = 1
	p.Func[0] = 0

	check := func(step string, want bool) {
		t.Helper()
		got := c.pioInputLevels()&1 != 0
		if got != want {
			t.Fatalf("%s: input 0 = %v, want %v", step, got, want)
		}
	}
	check("driven high", true)

	p.Out = 0
	check("output cleared", false)

	p.OE = 0
	p.ExtValid = 1
	p.ExtLevel = 1
	check("external level", true)
	p.ExtLevel = 0
	check("external low", false)

	p.ExtValid = 0
	p.pullUpMask = 1
	check("pull-up", true)
	p.pullUpMask = 0
	check("no pull", false)

	p.padIE &^= 1
	p.ExtValid = 1
	p.ExtLevel = 1
	check("input disabled", false)

	p.padIE |= 1
	check("input enabled", true)

	p.Out = 1
	p.OE = 1
	p.ExtValid = 0
	p.padOD = 1
	check("output disabled", false)
	p.padOD = 0
	check("output enabled", true)

	p.padISO = 1 // isolation latches the level captured at assert time (low)
	check("isolated", false)
	p.padISO = 0
	check("isolation released", true)
}

// TestRunChunkInvariance runs the same program in one call and in small
// chunks; the machine state must be identical, which guards any cache or
// fast path that depends on when it is refreshed.
func TestRunChunkInvariance(t *testing.T) {
	run := func(chunk uint64) uint64 {
		c := New()
		copy(c.SRAM, mmioProbeProgram)
		c.CPU0.R[13] = SRAMBase + 0x1000
		c.CPU0.R[15] = SRAMBase
		c.CPU1.Parked = true
		// SysTick counting, no interrupt; TIMER0 alarm armed, INTE clear.
		c.Write32(SCSBase+0x10, 1|4) // CTRL: ENABLE | CLKSOURCE
		c.Write32(SCSBase+0x14, 1000)
		c.Write32(SCSBase+0x18, 0)
		c.Write32(0x400b0010, 5000) // ALARM0
		const total = 200_000
		var done uint64
		for done < total {
			n := min(chunk, total-done)
			d := c.Run(n)
			if d == 0 {
				t.Fatalf("halted after %d cycles", done)
			}
			done += d
		}
		return hashMachine(c)
	}

	want := run(200_000)
	for _, chunk := range []uint64{1, 7, 1000, 65_536} {
		if got := run(chunk); got != want {
			t.Fatalf("chunk %d: state hash %016x, want %016x", chunk, got, want)
		}
	}
}
