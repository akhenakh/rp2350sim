package rp2350

import "fmt"

// usbBlock models the RP2350 USB device controller together with enough of the
// host side of USB to enumerate the device. TinyGo's default console is USB CDC,
// so `println` and panic output only work once a host has configured the CDC
// interface; here the emulator plays that host.
//
// The register window covers the 4 KB DPRAM at 0x50100000 and the controller
// registers at 0x50110000. DPRAM is plain memory: the firmware writes endpoint
// control blocks and buffers there, and the host engine moves packets in and
// out of the same bytes.
type usbBlock struct {
	chip  *Chip
	block *regBlock

	// Controller interrupt state (INTR), BUFF_STATUS latches and SIE_STATUS
	// bits that firmware writes 1 to in order to clear.
	intr       uint32
	buffStatus uint32
	sieStatus  uint32

	attached bool
	addr     uint32

	// Host enumeration state.
	state     usbHostState
	step      int
	wait      int
	outSent   bool
	collected []byte

	// OnCDC receives bytes the firmware sends on the CDC data endpoint.
	OnCDC func(b []byte)
}

type usbHostState int

const (
	usbHostDetached usbHostState = iota
	usbHostResetWait
	usbHostAfterReset
	usbHostControl
	usbHostRunning
)

const usbIRQ = 14

const (
	usbRegsBase  = 0x50110000
	usbDPRAMBase = 0x50100000
)

// Controller registers live 64 KB above the DPRAM window.
func usbReg(off uint32) uint32 { return 0x10000 + off }

const (
	usbADDRENDP   = 0x000
	usbMAINCTRL   = 0x040
	usbSIECTRL    = 0x04C
	usbSIESTATUS  = 0x050
	usbBUFFSTATUS = 0x058
	usbEPSTALLARM = 0x068
	usbINTR       = 0x08C
	usbINTE       = 0x090
	usbINTF       = 0x094
	usbINTS       = 0x098
)

// Interrupt bits in INTR/INTE/INTS.
const (
	usbINTBuffStatus = 1 << 4
	usbINTBusReset   = 1 << 12
	usbINTSetupReq   = 1 << 16
)

// SIE_STATUS bits; each is write-1-to-clear and clears its INT latch.
const (
	usbSIESetupRec = 0x00020000
	usbSIEReset    = 0x00080000
	usbSIEACKRec   = 0x40000000
)

const (
	usbSIEPullupEn = 0x00010000
	usbMainEnable  = 0x00000001
)

// DPRAM buffer-control bits for a direction's control register.
const (
	usbBufFull   = 0x8000
	usbBufData1  = 0x2000
	usbBufStall  = 0x0800
	usbBufAvail  = 0x0400
	usbBufLen    = 0x03FF
	usbBufLenOff = 0x400
)

// DPRAM layout helpers.
func usbBufCtrlOff(ep int) uint32 { return 0x80 + uint32(ep)*8 }
func usbBufDataOff(ep int, in bool) uint32 {
	off := 0x100 + uint32(ep)*128
	if ep != 0 && in {
		off += 64 // IN endpoints other than EP0 use Buffer1
	}
	return off
}

func newUSB(c *Chip) *usbBlock {
	u := &usbBlock{chip: c, block: newRegBlock(usbDPRAMBase, 0x11000)}
	u.block.readFn = u.read
	u.block.writeFn = u.write
	return u
}

func (u *usbBlock) read(c *Chip, off uint32) (uint32, bool) {
	switch off {
	case usbReg(usbADDRENDP):
		return u.addr, true
	case usbReg(usbSIESTATUS):
		return u.sieStatus, true
	case usbReg(usbBUFFSTATUS):
		return u.buffStatus, true
	case usbReg(usbINTR):
		return u.intr, true
	case usbReg(usbINTS):
		return u.intr & u.inte(), true
	}
	return 0, false
}

func (u *usbBlock) write(c *Chip, off, v uint32) {
	switch off {
	case usbReg(usbADDRENDP):
		u.addr = v & 0x7F
	case usbReg(usbSIESTATUS):
		// Write 1 to clear the latched event and its interrupt.
		if v&usbSIESetupRec != 0 {
			u.intr &^= usbINTSetupReq
		}
		if v&usbSIEReset != 0 {
			u.intr &^= usbINTBusReset
		}
		u.sieStatus &^= v & (usbSIESetupRec | usbSIEReset | usbSIEACKRec)
	case usbReg(usbBUFFSTATUS):
		u.buffStatus &^= v
		if u.buffStatus == 0 {
			u.intr &^= usbINTBuffStatus
		}
	case usbReg(usbINTS):
		u.intr &^= v
	case usbReg(usbSIECTRL):
		if v&usbSIEPullupEn != 0 && !u.attached {
			u.attach()
		}
	}
}

func (u *usbBlock) inte() uint32  { return u.block.regs[usbReg(usbINTE)/4] }
func (u *usbBlock) enabled() bool { return u.block.regs[usbReg(usbMAINCTRL)/4]&usbMainEnable != 0 }

func (u *usbBlock) attach() {
	u.attached = true
	u.state = usbHostResetWait
	u.wait = 2000 // let the device see a settled bus before the reset
}

// word reads a DPRAM word.
func (u *usbBlock) word(off uint32) uint32 { return u.block.regs[off/4] }

func (u *usbBlock) setWord(off, v uint32) { u.block.regs[off/4] = v }

func (u *usbBlock) writeBytes(off uint32, b []byte) {
	for i, v := range b {
		a := off + uint32(i)
		w := a / 4
		sh := (a & 3) * 8
		u.block.regs[w] = u.block.regs[w]&^(0xFF<<sh) | uint32(v)<<sh
	}
}

func (u *usbBlock) readBytes(off uint32, n int) []byte {
	b := make([]byte, n)
	for i := 0; i < n; i++ {
		a := off + uint32(i)
		b[i] = byte(u.block.regs[a/4] >> ((a & 3) * 8))
	}
	return b
}

func (u *usbBlock) raiseBuff(bits uint32) {
	u.buffStatus |= bits
	u.intr |= usbINTBuffStatus
}

func (u *usbBlock) setSIEStatus(bits uint32) {
	u.sieStatus |= bits
}

func (u *usbBlock) logf(format string, args ...any) {
	if u.chip.USBLog != nil {
		fmt.Fprintf(u.chip.USBLog, format, args...)
	}
}

// setupRequest is one control transfer the emulated host performs.
type setupRequest struct {
	reqType byte
	request byte
	value   uint16
	index   uint16
	length  uint16
}

func (r setupRequest) bytes() [8]byte {
	return [8]byte{
		r.reqType, r.request,
		byte(r.value), byte(r.value >> 8),
		byte(r.index), byte(r.index >> 8),
		byte(r.length), byte(r.length >> 8),
	}
}

// hostEnumeration is the sequence a Linux host performs for a CDC ACM device.
var hostEnumeration = []setupRequest{
	{0x80, 6, 0x0100, 0, 64},  // GET_DESCRIPTOR device
	{0x00, 5, 1, 0, 0},        // SET_ADDRESS 1
	{0x80, 6, 0x0100, 0, 18},  // GET_DESCRIPTOR device (full)
	{0x80, 6, 0x0200, 0, 9},   // GET_DESCRIPTOR configuration header
	{0x80, 6, 0x0200, 0, 255}, // GET_DESCRIPTOR configuration
	{0x80, 6, 0x0300, 0, 255}, // GET_DESCRIPTOR string 0
	{0x80, 6, 0x0301, 0, 255}, // manufacturer
	{0x80, 6, 0x0302, 0, 255}, // product
	{0x80, 6, 0x0303, 0, 255}, // serial
	{0x00, 9, 1, 0, 0},        // SET_CONFIGURATION 1
	{0x21, 0x22, 0x03, 0, 0},  // SET_CONTROL_LINE_STATE: DTR | RTS
	{0xA1, 0x21, 0, 0, 7},     // GET_LINE_CODING
}

// tick advances the host state machine. It is called once per chip cycle.
func (u *usbBlock) tick(c *Chip) {
	if u.attached && u.intr&u.inte() != 0 {
		c.NVIC.Pend(usbIRQ)
	}
	if !u.attached || !u.enabled() {
		return
	}
	switch u.state {
	case usbHostResetWait:
		if u.wait--; u.wait <= 0 {
			u.setSIEStatus(usbSIEReset)
			u.intr |= usbINTBusReset
			u.state = usbHostAfterReset
			u.wait = 100
		}
	case usbHostAfterReset:
		if u.wait--; u.wait <= 0 {
			u.step = 0
			u.beginStep()
		}
	case usbHostControl:
		u.pollControl()
	case usbHostRunning:
		u.pollEndpoints()
	}
}

func (u *usbBlock) beginStep() {
	if u.step >= len(hostEnumeration) {
		u.state = usbHostRunning
		u.logf("usb: enumeration complete\n")
		return
	}
	req := hostEnumeration[u.step]
	setup := req.bytes()
	u.writeBytes(0, setup[:4])
	u.writeBytes(4, setup[4:])
	u.intr |= usbINTSetupReq
	u.outSent = false
	u.collected = u.collected[:0]
	u.state = usbHostControl
	u.logf("usb: setup %02x %02x value=%04x index=%d len=%d\n",
		req.reqType, req.request, req.value, req.index, req.length)
}

// epInFull reports whether the firmware has armed the IN buffer of an endpoint.
func (u *usbBlock) epInFull(ep int) bool {
	return u.word(usbBufCtrlOff(ep))&usbBufFull != 0
}

// takeIN consumes one IN packet: it returns the payload and acknowledges the
// packet to the controller (clearing the buffer and raising BUFF_STATUS).
func (u *usbBlock) takeIN(ep int) []byte {
	ctrl := u.word(usbBufCtrlOff(ep))
	n := int(ctrl & usbBufLen)
	data := u.readBytes(usbBufDataOff(ep, true), n)
	u.logf("usb: in ep%d len=%d\n", ep, n)
	u.setWord(usbBufCtrlOff(ep), 0)
	u.raiseBuff(1 << uint(ep*2))
	return data
}

func (u *usbBlock) pollControl() {
	req := hostEnumeration[u.step]
	in := req.reqType&0x80 != 0
	if in {
		if !u.epInFull(0) {
			return
		}
		data := u.takeIN(0)
		u.collected = append(u.collected, data...)
		// A short packet ends the data stage.
		if len(data) < 64 || len(u.collected) >= int(req.length) {
			u.finishStep()
		}
		return
	}

	if req.length > 0 && !u.outSent {
		// Host-to-device data stage, e.g. SET_LINE_CODING. The payload is not
		// needed by the emulated host yet; send a zeroed line-coding record so
		// the firmware's parser sees a valid shape.
		payload := make([]byte, req.length)
		u.writeBytes(usbBufDataOff(0, false), payload)
		u.setWord(usbBufCtrlOff(0), uint32(req.length)|usbBufData1|usbBufFull)
		u.raiseBuff(1 << 1) // EP0 OUT
		u.outSent = true
		return
	}

	// Status stage: the firmware arms an IN zero-length packet.
	if !u.epInFull(0) {
		return
	}
	u.takeIN(0)
	if req.request == 5 { // SET_ADDRESS: the firmware polls ACK_REC
		u.setSIEStatus(usbSIEACKRec)
	}
	u.finishStep()
}

func (u *usbBlock) finishStep() {
	if u.step < len(hostEnumeration) {
		req := hostEnumeration[u.step]
		if req.request == 6 && len(u.collected) >= 8 {
			u.logf("usb: descriptor %04x len=%d head=% x\n",
				req.value, len(u.collected), u.collected[:min(8, len(u.collected))])
		}
	}
	u.step++
	if u.step < len(hostEnumeration) {
		u.beginStep()
		return
	}
	u.state = usbHostRunning
	u.logf("usb: enumeration complete\n")
}

// pollEndpoints services the CDC data endpoints once enumeration is done: every
// IN packet the firmware arms is drained and acknowledged.
func (u *usbBlock) pollEndpoints() {
	for ep := 0; ep < 8; ep++ {
		if !u.epInFull(ep) {
			continue
		}
		data := u.takeIN(ep)
		if len(data) > 0 && u.OnCDC != nil {
			u.OnCDC(data)
		}
	}
}

// SendCDC queues a packet for the host to deliver to the device's CDC OUT
// endpoint (endpoint 2). The UI and console input will use it.
func (u *usbBlock) SendCDC(data []byte) {
	if len(data) > 64 {
		data = data[:64]
	}
	u.writeBytes(usbBufDataOff(2, false), data)
	u.setWord(usbBufCtrlOff(2), uint32(len(data))|usbBufFull)
	u.raiseBuff(1 << 5) // EP2 OUT
}
