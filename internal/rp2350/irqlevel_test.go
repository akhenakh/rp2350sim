package rp2350

import "testing"

// TestLevelIRQRependsOnReturn: hardware re-asserts a level-sensitive line that
// is still active when its handler returns. An ISR that forgets to clear the
// source must be entered again; one that clears it must not.
func TestLevelIRQRependsOnReturn(t *testing.T) {
	t.Run("pwm", func(t *testing.T) {
		c := New()
		c.Write32(pwmBase+pwmInte0, 1)
		c.pwm.raiseWrap(0)
		if c.ppb[0].pending[0]&(1<<pwmIRQ0) == 0 {
			t.Fatal("wrap did not pend")
		}
		c.NVIC.ReturnException(16 + pwmIRQ0)
		if c.ppb[0].pending[0]&(1<<pwmIRQ0) == 0 {
			t.Fatal("active wrap line was not re-pended")
		}
		c.Write32(pwmBase+pwmIntrOffset, 1)
		c.NVIC.ReturnException(16 + pwmIRQ0)
		if c.ppb[0].pending[0]&(1<<pwmIRQ0) != 0 {
			t.Fatal("cleared wrap line stayed pending")
		}
	})

	t.Run("timer", func(t *testing.T) {
		c := New()
		tm := &c.tmr[0]
		tm.intE = 1
		tm.intR = 1
		tm.pend()
		if c.ppb[0].pending[0]&1 == 0 {
			t.Fatal("timer IRQ did not pend")
		}
		c.NVIC.ReturnException(16 + 0)
		if c.ppb[0].pending[0]&1 == 0 {
			t.Fatal("active timer line was not re-pended")
		}
		c.Write32(timer0Base+0x3C, 1)
		c.NVIC.ReturnException(16 + 0)
		if c.ppb[0].pending[0]&1 != 0 {
			t.Fatal("cleared timer line stayed pending")
		}
	})

	t.Run("uart", func(t *testing.T) {
		const base = 0x40070000
		c := New()
		c.Write32(base+0x2C, 1<<4) // FEN
		c.Write32(base+0x34, 2)    // RX threshold: one eighth (4 bytes)
		c.Write32(base+0x38, 1<<4) // RXIM
		c.SendUART(0, []byte{1, 2, 3, 4})
		c.uart[0].tick()
		if c.ppb[0].pending[1]&(1<<(uartRXIRQ-32)) == 0 {
			t.Fatal("UART IRQ did not pend")
		}
		c.NVIC.ReturnException(16 + uartRXIRQ)
		if c.ppb[0].pending[1]&(1<<(uartRXIRQ-32)) == 0 {
			t.Fatal("non-empty RX line was not re-pended")
		}
		for range 4 {
			c.Read32(base) // drain the FIFO below the threshold
		}
		c.NVIC.ReturnException(16 + uartRXIRQ)
		if c.ppb[0].pending[1]&(1<<(uartRXIRQ-32)) != 0 {
			t.Fatal("drained RX line stayed pending")
		}
	})
}
