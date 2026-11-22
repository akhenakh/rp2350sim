package rp2350

// POWMAN hosts the always-on (AON) 1 kHz timer at 0x40100000. The model
// implements the millisecond counter, set/read time, alarm and timer interrupt
// used by pico_aon_timer; the power-management state machine itself is not
// modelled.
const powmanBase = 0x40100000

const (
	powmanSetTime0  = 0x60 // 63:48, then 47:32, 31:16, 15:0 at 0x6c
	powmanReadUpper = 0x70
	powmanReadLower = 0x74
	powmanAlarm0    = 0x78
	powmanTimer     = 0x88
	powmanXOSCInt   = 0x58
	powmanXOSCFrac  = 0x5C
	powmanINTE      = 0xE4
	powmanINTF      = 0xE8
	powmanINTS      = 0xEC
	powmanIRQ       = 45
)

const (
	powmanTimerRun       = 1 << 1
	powmanTimerClear     = 1 << 2
	powmanTimerAlarmEnab = 1 << 4
	powmanTimerPwrUp     = 1 << 5
	powmanTimerFired     = 1 << 6
	powmanTimerUseXOSC   = 1 << 9
	powmanTimerUsingXOSC = 1 << 16
	powmanIntTimer       = 1 << 1
)

type powmanBlock struct {
	block *regBlock

	setTime  uint64
	alarm    uint64
	timeBase uint64
	baseCyc  uint64
	running  bool
	useXOSC  bool
	alarmOn  bool
	pwrUp    bool
	fired    bool

	inte uint32
	intf uint32
}

func newPOWMAN() *powmanBlock {
	p := &powmanBlock{block: newRegBlock(powmanBase, 0x1000)}
	p.block.readFn = p.read
	p.block.writeFn = p.write
	p.block.resetFn = func(c *Chip) { p.reset() }
	return p
}

func (p *powmanBlock) reset() {
	p.setTime, p.alarm, p.timeBase, p.baseCyc = 0, 0, 0, 0
	p.running, p.useXOSC, p.alarmOn, p.pwrUp, p.fired = false, false, false, false, false
	p.inte, p.intf = 0, 0
}

// nowMS returns the timer's millisecond count. The AON timer ticks from the
// 150 MHz clock at 1 kHz, giving 150_000 cycles per millisecond.
func (p *powmanBlock) nowMS(c *Chip) uint64 {
	if !p.running {
		return p.timeBase
	}
	return p.timeBase + (c.Cycles-p.baseCyc)/150_000
}

func (p *powmanBlock) read(c *Chip, off uint32) (uint32, bool) {
	switch off {
	case powmanReadUpper:
		return uint32(p.nowMS(c) >> 32), true
	case powmanReadLower:
		return uint32(p.nowMS(c)), true
	case powmanTimer:
		var v uint32
		if p.running {
			v |= powmanTimerRun
		}
		if p.alarmOn {
			v |= powmanTimerAlarmEnab
		}
		if p.pwrUp {
			v |= powmanTimerPwrUp
		}
		if p.fired {
			v |= powmanTimerFired
		}
		if p.useXOSC {
			v |= powmanTimerUseXOSC | powmanTimerUsingXOSC
		} else if p.running {
			v |= 1 << 17 // USING_LPOSC
		}
		return v, true
	case powmanINTE:
		return p.inte, true
	case powmanINTF:
		return p.intf, true
	case powmanINTS:
		return p.intf & p.inte, true
	case powmanXOSCInt:
		return 0, true
	case powmanXOSCFrac:
		return 0, true
	}
	return 0, false
}

func (p *powmanBlock) write(c *Chip, off, v uint32) {
	switch off {
	case powmanSetTime0, powmanSetTime0 + 4, powmanSetTime0 + 8, powmanSetTime0 + 12:
		shift := (3 - (off-powmanSetTime0)/4) * 16
		p.setTime = p.setTime&^(0xFFFF<<shift) | uint64(v&0xFFFF)<<shift
		if !p.running {
			p.timeBase = p.setTime
		}
	case powmanAlarm0, powmanAlarm0 + 4, powmanAlarm0 + 8, powmanAlarm0 + 12:
		shift := (3 - (off-powmanAlarm0)/4) * 16
		p.alarm = p.alarm&^(0xFFFF<<shift) | uint64(v&0xFFFF)<<shift
	case powmanTimer:
		wasRunning := p.running
		p.running = v&powmanTimerRun != 0
		if p.running && !wasRunning {
			p.baseCyc = c.Cycles
			p.timeBase = p.setTime
		} else if !p.running && wasRunning {
			p.timeBase = p.nowMS(c)
		}
		if v&powmanTimerClear != 0 {
			p.timeBase = 0
			p.baseCyc = c.Cycles
		}
		p.alarmOn = v&powmanTimerAlarmEnab != 0
		p.pwrUp = v&powmanTimerPwrUp != 0
		if v&powmanTimerUseXOSC != 0 {
			p.useXOSC = true
		}
		if v&powmanTimerFired != 0 { // write 1 to clear the fired alarm
			p.fired = false
			p.intf &^= powmanIntTimer
		}
	case powmanINTE:
		p.inte = v
	case powmanINTF:
		p.intf &^= v
	case powmanINTS:
		p.intf &^= v
	default:
		// Other POWMAN registers are accepted and read back as zero.
	}
}

// tick runs once per emulated microsecond and raises the timer interrupt when
// the alarm time has been reached.
func (p *powmanBlock) tick(c *Chip) {
	if p.alarmOn && !p.fired && p.nowMS(c) >= p.alarm {
		p.fired = true
		p.intf |= powmanIntTimer
	}
	if p.intf&p.inte&powmanIntTimer != 0 {
		c.NVIC.Pend(powmanIRQ)
	}
}
