package rp2350

import (
	"fmt"
	"log/slog"
)

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
	chip *Chip
	// dpram is the 4 KB endpoint buffer memory. The controller registers live
	// in their own block so the RP2350 atomic aliases at +0x1000/+0x2000/
	// +0x3000 resolve to the registers, not to DPRAM.
	dpram *regBlock
	regs  *regBlock

	// Controller interrupt state (INTR), BUFF_STATUS latches and SIE_STATUS
	// bits that firmware writes 1 to in order to clear.
	intr       uint32
	intf       uint32
	buffStatus uint32
	buffCPU    uint32
	sieStatus  uint32
	irqActive  bool

	// The controller's double-buffer selector is private. Completion pending
	// bits model BUFF_STATUS reasserting after a W1C when the other buffer has
	// also completed.
	bufSelect      [16][2]bool // endpoint, direction (IN=0, OUT=1)
	buffPending    [16][2]uint8
	bufCompletions [16][2]uint8

	attached bool
	addr     uint32
	// midi enables the USB-MIDI bulk endpoint, as used by TinyGo's CDCMIDI
	// composite descriptor and by pico-sdk TinyUSB MIDI descriptors.
	midi bool

	// Endpoint numbers discovered from the configuration descriptor. Defaults
	// match TinyGo's CDC (EP1 notification, EP2 data) and CDCMIDI (EP3 MIDI)
	// so a run still works if the descriptor is truncated or malformed.
	cdcNotify int
	cdcIn     int
	cdcOut    int
	midiIn    int
	midiOut   int

	// Host enumeration state.
	state    usbHostState
	step     int
	wait     int
	pollTick int
	outSent  bool
	// needStatus/statusSent track the OUT zero-length status packet that ends
	// a control-IN data stage (TinyUSB requires it to finish the request).
	needStatus bool
	statusSent bool

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

const (
	usbADDRENDP    = 0x000
	usbMAINCTRL    = 0x040
	usbSIECTRL     = 0x04C
	usbSIESTATUS   = 0x050
	usbBUFFSTATUS  = 0x058
	usbBUFFCPU     = 0x05C
	usbEPSTALLARM  = 0x068
	usbNAKPOLL     = 0x06C
	usbMUXING      = 0x074
	usbINTR        = 0x08C
	usbINTE        = 0x090
	usbINTF        = 0x094
	usbINTS        = 0x098
	usbIRQMask     = 0x00FFFFFF
	usbMainPHYISO  = 1 << 2
	usbSIEPullDown = 1 << 15
	usbNAKPollRst  = 0x00100010
	usbMuxToPHY    = 1
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
	usbBufLast   = 0x4000
	usbBufData1  = 0x2000
	usbBufSel    = 0x1000
	usbBufStall  = 0x0800
	usbBufAvail  = 0x0400
	usbBufLen    = 0x03FF
	usbBufLenOff = 0x400
	usbBuf1Shift = 16
)

// DPRAM endpoint-control registers start at EP1 (EP0 has fixed buffers).
const (
	usbEpCtrlBase    = 0x008
	usbEpCtrlEnable  = 1 << 31
	usbEpCtrlDouble  = 1 << 30
	usbEpCtrlInt1Buf = 1 << 29
	usbEpCtrlInt2Buf = 1 << 28
	usbEpCtrlAddr    = 0xFFC0
	usbEpDataBase    = 0x180 // end of EP0 buffers, start of the shared pool
	usbEp0Buf        = 0x100
	usbEp0Buf1       = 0x140
	usbDPSRAMSize    = 0x1000
	usbSIEEP0Double  = 1 << 30
	usbSIEEP0Int1Buf = 1 << 29
	usbSIEEP0Int2Buf = 1 << 28
)

// DPRAM layout helpers.
func usbBufCtrlOff(ep int) uint32 { return 0x80 + uint32(ep)*8 } // IN buffer control
// usbBufCtrlOffOut is the OUT buffer control word; each endpoint has an IN
// control at +0 and an OUT control at +4. Writing a host-to-device payload to
// the IN register makes the guest read the configured 64-byte buffer size
// instead of the transfer length (and arms a phantom IN packet).
func usbBufCtrlOffOut(ep int) uint32 { return usbBufCtrlOff(ep) + 4 }

// usbBufDataOff is the TinyGo buffer layout, used as the fallback when the
// firmware has not programmed EP_CONTROL or the address is out of range.
func usbBufDataOff(ep int, in bool) uint32 {
	off := 0x100 + uint32(ep)*128
	if ep != 0 && in {
		off += 64 // IN endpoints other than EP0 use Buffer1
	}
	return off
}

func usbEpCtrlOff(ep int, in bool) uint32 {
	off := uint32(usbEpCtrlBase + (ep-1)*8)
	if !in {
		off += 4
	}
	return off
}

func usbDirection(in bool) int {
	if in {
		return 0
	}
	return 1
}

func usbBufferBit(ep int, in bool) uint32 {
	return 1 << uint(ep*2+usbDirection(in))
}

func usbBufferHalf(ctrl uint32, buf int) uint32 {
	if buf == 0 {
		return ctrl & 0xFFFF
	}
	return ctrl >> usbBuf1Shift
}

func usbSetBufferHalf(ctrl uint32, buf int, half uint32) uint32 {
	if buf == 0 {
		return ctrl&0xFFFF0000 | half&0xFFFF
	}
	return ctrl&0xFFFF | half<<usbBuf1Shift
}

// epDataAddr reads the directional buffer address programmed in EP_CONTROL.
// TinyUSB allocates endpoint buffers dynamically and exposes the DPRAM offset
// there; TinyGo programs the same register with its fixed layout.
func (u *usbBlock) epDataAddr(ep int, in bool) uint32 {
	if ep == 0 {
		return usbEp0Buf
	}
	ctrl := u.word(usbEpCtrlOff(ep, in))
	// Bits 0..5 are ignored by the controller. Each direction has its own
	// geometry: OUT may be double buffered even when IN is not, or vice versa.
	need := uint32(64)
	if u.epDouble(ep, in) {
		need = 128
	}
	if ctrl&usbEpCtrlEnable != 0 {
		if addr := ctrl & usbEpCtrlAddr; addr >= usbEpDataBase && addr+need <= usbDPSRAMSize {
			return addr
		}
	}
	return usbBufDataOff(ep, in)
}

func (u *usbBlock) bufferAddr(ep int, in bool, buf int) uint32 {
	addr := u.epDataAddr(ep, in)
	if buf == 1 {
		if ep == 0 {
			return usbEp0Buf1
		}
		return addr + 64
	}
	return addr
}

// epDouble reports whether this direction's EP_CONTROL enables its second
// buffer. EP0 has no EP_CONTROL word; SIE_CTRL owns its geometry.
func (u *usbBlock) epDouble(ep int, in bool) bool {
	if ep == 0 {
		return u.regs.regs[usbSIECTRL/4]&usbSIEEP0Double != 0
	}
	return u.word(usbEpCtrlOff(ep, in))&usbEpCtrlDouble != 0
}

func (u *usbBlock) selectedBuffer(ep int, in bool) int {
	if u.epDouble(ep, in) && u.bufSelect[ep][usbDirection(in)] {
		return 1
	}
	return 0
}

func (u *usbBlock) epIntEveryBuffer(ep int, in bool) bool {
	if ep == 0 {
		return u.regs.regs[usbSIECTRL/4]&usbSIEEP0Int1Buf != 0
	}
	return u.word(usbEpCtrlOff(ep, in))&usbEpCtrlInt1Buf != 0
}

func (u *usbBlock) epIntEveryTwoBuffers(ep int, in bool) bool {
	if ep == 0 {
		return u.regs.regs[usbSIECTRL/4]&usbSIEEP0Int2Buf != 0
	}
	return u.word(usbEpCtrlOff(ep, in))&usbEpCtrlInt2Buf != 0
}

// epInReady reports whether the selected IN buffer is fully armed. TinyUSB
// writes the control twice (first without AVAIL, then with AVAIL after a settle
// delay); ACKing the intermediate write would send the same packet twice.
func (u *usbBlock) epInReady(ep int) bool {
	ctrl := u.word(usbBufCtrlOff(ep))
	buf := u.selectedBuffer(ep, true)
	return usbBufferReady(usbBufferHalf(ctrl, buf))
}

func usbBufferReady(ctrl uint32) bool {
	return ctrl&usbBufFull != 0 && ctrl&usbBufAvail != 0
}

// epOutReady reports whether the selected OUT buffer is armed and empty.
func (u *usbBlock) epOutReady(ep int) bool {
	ctrl := u.word(usbBufCtrlOffOut(ep))
	half := usbBufferHalf(ctrl, u.selectedBuffer(ep, false))
	return half&usbBufAvail != 0 && half&usbBufFull == 0
}

func (u *usbBlock) setEndpointDefaults() {
	u.cdcNotify, u.cdcIn, u.cdcOut = 1, 2, 2
	u.midiIn, u.midiOut = 3, 3
}

func (u *usbBlock) reset() {
	u.intr, u.intf = 0, 0
	u.buffStatus, u.buffCPU = 0, 0
	u.sieStatus = 0
	u.bufSelect = [16][2]bool{}
	u.buffPending = [16][2]uint8{}
	u.bufCompletions = [16][2]uint8{}
	u.irqActive = false
	if u.chip != nil {
		u.chip.ClearPendingIRQ(usbIRQ)
	}
	u.regs.regs[usbMAINCTRL/4] = usbMainPHYISO
	u.regs.regs[usbSIECTRL/4] = usbSIEPullDown
	u.regs.regs[usbNAKPOLL/4] = usbNAKPollRst
	u.regs.regs[usbMUXING/4] = usbMuxToPHY
	u.attached = false
	u.addr = 0
	u.state = usbHostDetached
	u.step = 0
	u.wait = 0
	u.pollTick = 0
	u.outSent = false
	u.needStatus = false
	u.statusSent = false
	u.collected = u.collected[:0]
	u.setEndpointDefaults()
}

func newUSB(c *Chip) *usbBlock {
	u := &usbBlock{chip: c}
	u.dpram = newRegBlock(usbDPRAMBase, 0x1000)
	u.dpram.noAliases = true
	u.dpram.writeFn = u.writeDPRAM
	u.regs = newRegBlock(usbRegsBase, 0x1000)
	u.regs.readFn = u.read
	u.regs.writeFn = u.write
	u.regs.clearFn = u.clear
	u.regs.resetFn = func(c *Chip) { u.reset() }
	u.setEndpointDefaults()
	return u
}

func (u *usbBlock) read(c *Chip, off uint32) (uint32, bool) {
	switch off {
	case usbADDRENDP:
		return u.addr, true
	case usbSIESTATUS:
		return u.sieStatus, true
	case usbBUFFSTATUS:
		return u.buffStatus, true
	case usbBUFFCPU:
		return u.buffCPU, true
	case usbINTR:
		return u.intr, true
	case usbINTF:
		return u.intf, true
	case usbINTS:
		return u.ints(), true
	}
	return 0, false
}

func (u *usbBlock) write(c *Chip, off, v uint32) {
	switch off {
	case usbADDRENDP:
		u.addr = v & 0x7F
	case usbSIESTATUS:
		// Write 1 to clear the latched event and its interrupt.
		if v&usbSIESetupRec != 0 {
			u.intr &^= usbINTSetupReq
		}
		if v&usbSIEReset != 0 {
			u.intr &^= usbINTBusReset
		}
		u.sieStatus &^= v & (usbSIESetupRec | usbSIEReset | usbSIEACKRec)
	case usbBUFFSTATUS:
		u.clearBuffStatus(v)
	case usbINTF:
		u.intf = v & usbIRQMask
	case usbINTS:
		// Read-only status after masking and forcing.
	case usbSIECTRL:
		if v&usbSIEPullupEn != 0 && !u.attached {
			u.attach()
		}
	}
	u.syncIRQ(c)
}

// clear handles the +0x3000 CLR alias. The controller's status and interrupt
// registers are write-1-to-clear, so they need the written mask; plain control
// registers take the usual read-modify-write result.
func (u *usbBlock) clear(c *Chip, off, v uint32) {
	switch off {
	case usbSIESTATUS, usbBUFFSTATUS, usbINTS:
		u.write(c, off, v)
	case usbINTF:
		u.intf &^= v
	default:
		u.regs.regs[off/4] &^= v
	}
	u.syncIRQ(c)
}

func (u *usbBlock) inte() uint32  { return u.regs.regs[usbINTE/4] & usbIRQMask }
func (u *usbBlock) ints() uint32  { return (u.intr | u.intf) & u.inte() }
func (u *usbBlock) enabled() bool { return u.regs.regs[usbMAINCTRL/4]&usbMainEnable != 0 }

func (u *usbBlock) syncIRQ(c *Chip) {
	active := u.ints() != 0
	if active == u.irqActive {
		return
	}
	u.irqActive = active
	if active {
		c.PendIRQ(usbIRQ)
		return
	}
	c.ClearPendingIRQ(usbIRQ)
}

// idleForSkip reports whether the USB host cannot be mid-handshake, so a
// microsecond jump will not disturb it. Enumeration waits on tick counters;
// once running, the endpoint poll is periodic and a coarser tick only adds
// latency well below a USB frame.
func (u *usbBlock) idleForSkip() bool {
	if !u.attached || !u.enabled() {
		return true
	}
	return u.state == usbHostRunning
}

func (u *usbBlock) attach() {
	u.attached = true
	u.state = usbHostResetWait
	u.wait = 2000 // let the device see a settled bus before the reset
}

// word reads a DPRAM word.
func (u *usbBlock) word(off uint32) uint32 { return u.dpram.regs[off/4] }

func (u *usbBlock) setWord(off, v uint32) { u.dpram.regs[off/4] = v }

func (u *usbBlock) writeDPRAM(c *Chip, off, v uint32) {
	if off < usbBufCtrlOff(0) || off >= 0x100 {
		return
	}
	ep := int((off - usbBufCtrlOff(0)) / 8)
	in := off&4 == 0
	if u.word(off)&usbBufSel != 0 && u.epDouble(ep, in) {
		u.bufSelect[ep][usbDirection(in)] = false
	}
}

func (u *usbBlock) writeBytes(off uint32, b []byte) {
	for i, v := range b {
		a := off + uint32(i)
		if a >= usbDPSRAMSize {
			return
		}
		w := a / 4
		sh := (a & 3) * 8
		u.dpram.regs[w] = u.dpram.regs[w]&^(0xFF<<sh) | uint32(v)<<sh
	}
}

func (u *usbBlock) readBytes(off uint32, n int) []byte {
	b := make([]byte, n)
	for i := range n {
		a := off + uint32(i)
		if a >= usbDPSRAMSize {
			break
		}
		b[i] = byte(u.dpram.regs[a/4] >> ((a & 3) * 8))
	}
	return b
}

func (u *usbBlock) syncBuffIRQ(c *Chip) {
	if u.buffStatus != 0 {
		u.intr |= usbINTBuffStatus
	} else {
		u.intr &^= usbINTBuffStatus
	}
	u.syncIRQ(c)
}

func (u *usbBlock) completeBuffer(ep int, in bool, buf int) {
	dir := usbDirection(in)
	if u.epDouble(ep, in) {
		u.bufSelect[ep][dir] = buf == 0
	} else {
		u.bufSelect[ep][dir] = false
	}
	u.bufCompletions[ep][dir]++
	bit := usbBufferBit(ep, in)
	if u.epIntEveryBuffer(ep, in) {
		u.buffPending[ep][dir] |= 1 << uint(buf)
		if u.buffStatus&bit == 0 {
			u.buffStatus |= bit
			if buf == 1 {
				u.buffCPU |= bit
			} else {
				u.buffCPU &^= bit
			}
		}
	} else if u.epDouble(ep, in) && u.epIntEveryTwoBuffers(ep, in) && u.bufCompletions[ep][dir]&1 == 0 {
		u.buffStatus |= bit
		u.buffCPU &^= bit
	}
	u.syncBuffIRQ(u.chip)
}

func (u *usbBlock) clearBuffStatus(v uint32) {
	for ep := range 16 {
		for dir := range 2 {
			in := dir == 0
			bit := usbBufferBit(ep, in)
			if v&bit == 0 || u.buffStatus&bit == 0 {
				continue
			}
			u.buffStatus &^= bit
			if u.epIntEveryBuffer(ep, in) {
				handled := 0
				if u.buffCPU&bit != 0 {
					handled = 1
				}
				u.buffPending[ep][dir] &^= 1 << uint(handled)
			}
			if u.buffPending[ep][dir] != 0 {
				u.buffStatus |= bit
				if u.buffPending[ep][dir]&1 != 0 {
					u.buffCPU &^= bit
				} else {
					u.buffCPU |= bit
				}
			} else {
				u.buffCPU &^= bit
			}
		}
	}
	u.syncBuffIRQ(u.chip)
}

func (u *usbBlock) setSIEStatus(bits uint32) {
	u.sieStatus |= bits
	if bits&usbSIESetupRec != 0 {
		u.intr |= usbINTSetupReq
		u.regs.regs[usbEPSTALLARM/4] &^= 3
		u.bufSelect[0] = [2]bool{}
	}
	if bits&usbSIEReset != 0 {
		u.intr |= usbINTBusReset
	}
	u.syncIRQ(u.chip)
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
	u.syncIRQ(c)
	if !u.attached || !u.enabled() {
		return
	}
	switch u.state {
	case usbHostResetWait:
		if u.wait--; u.wait <= 0 {
			u.setSIEStatus(usbSIEReset)
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
		// USB full-speed packets are microseconds apart; sampling the
		// endpoints every 64 cycles is far finer than needed and keeps the
		// idle host cheap.
		u.pollTick++
		if u.pollTick >= 64 {
			u.pollTick = 0
			u.pollEndpoints(c)
		}
	}
}

func (u *usbBlock) beginStep() {
	if u.step >= len(hostEnumeration) {
		u.state = usbHostRunning
		u.chip.Logger.Debug("usb enumeration complete")
		return
	}
	req := hostEnumeration[u.step]
	setup := req.bytes()
	u.writeBytes(0, setup[:4])
	u.writeBytes(4, setup[4:])
	u.setSIEStatus(usbSIESetupRec)
	u.outSent = false
	u.needStatus = false
	u.statusSent = false
	u.collected = u.collected[:0]
	u.state = usbHostControl
	u.chip.Logger.Debug("usb setup request",
		slog.Int("type", int(req.reqType)),
		slog.Int("request", int(req.request)),
		slog.Int("value", int(req.value)),
		slog.Int("index", int(req.index)),
		slog.Int("length", int(req.length)))
}

// takeIN consumes the selected IN buffer and performs the controller's status
// writeback. LENGTH, LAST and the next DATA PID survive; controller-owned
// FULL/AVAIL/SEL and STALL are cleared.
func (u *usbBlock) takeIN(ep int) []byte {
	if !u.epInReady(ep) {
		return nil
	}
	off := usbBufCtrlOff(ep)
	ctrl := u.word(off)
	buf := u.selectedBuffer(ep, true)
	half := usbBufferHalf(ctrl, buf)
	n := min(int(half&usbBufLen), 64)
	data := u.readBytes(u.bufferAddr(ep, true, buf), n)
	keep := half & (usbBufLen | usbBufLast)
	if half&usbBufData1 == 0 {
		keep |= usbBufData1
	}
	u.setWord(off, usbSetBufferHalf(ctrl, buf, keep))
	u.completeBuffer(ep, true, buf)
	u.chip.Logger.Debug("usb in packet", slog.Int("ep", ep), slog.Int("bytes", len(data)))
	return data
}

func (u *usbBlock) writeEP0OUT(n int, data1 bool) {
	buf := u.selectedBuffer(0, false)
	ctrl := u.word(usbBufCtrlOffOut(0))
	half := uint32(n) | usbBufFull
	if data1 {
		half |= usbBufData1
	}
	u.setWord(usbBufCtrlOffOut(0), usbSetBufferHalf(ctrl, buf, half))
	u.completeBuffer(0, false, buf)
}

func (u *usbBlock) pollControl() {
	req := hostEnumeration[u.step]
	in := req.reqType&0x80 != 0

	// A control-IN data stage ends with an OUT zero-length status packet from
	// the host. TinyUSB arms the opposite direction and only completes the
	// request (and processes the next SETUP) once that packet arrives.
	if u.needStatus {
		ctrl := u.word(usbBufCtrlOffOut(0))
		if !u.statusSent {
			// Wait until the firmware acknowledged the IN completion: the
			// status buffer is armed from inside that same BUFF_STATUS ISR.
			// TinyUSB's EP0 OUT starts unarmed and gets AVAIL when the stack
			// arms the status; TinyGo leaves it armed, so AVAIL is enough.
			if u.buffStatus&1 != 0 {
				return
			}
			half := usbBufferHalf(ctrl, u.selectedBuffer(0, false))
			if half&usbBufAvail == 0 || half&usbBufFull != 0 {
				return
			}
			u.writeEP0OUT(0, true)
			u.statusSent = true
			return
		}
		// The firmware clears the BUFF_STATUS bit when it handles the status;
		// TinyUSB does not clear FULL afterwards, so the bit is the handshake.
		if u.buffStatus&(1<<1) != 0 {
			return
		}
		u.needStatus = false
		u.statusSent = false
		u.finishStep()
		return
	}

	if in {
		// Do not complete the next packet while the firmware has not yet
		// acknowledged the previous IN completion: a multi-packet control
		// transfer (a config descriptor over 128 bytes) sends its next chunk
		// from the BUFF_STATUS interrupt, and a second completion raised
		// before that interrupt reads the register would be cleared unseen.
		if u.buffStatus&1 != 0 {
			return
		}
		if !u.epInReady(0) {
			return
		}
		data := u.takeIN(0)
		u.collected = append(u.collected, data...)
		// A short packet ends the data stage. A zero-length request is the
		// firmware's IN status packet for an OUT request.
		if len(data) < 64 || len(u.collected) >= int(req.length) {
			if req.length == 0 {
				u.finishStep()
				return
			}
			u.needStatus = true
		}
		return
	}

	if req.length > 0 && !u.outSent {
		// Host-to-device data stage, e.g. SET_LINE_CODING. Wait until the
		// firmware has serviced the SETUP request and armed EP0 OUT, then send
		// the first packet as DATA1.
		if u.intr&usbINTSetupReq != 0 {
			return
		}
		ctrl := u.word(usbBufCtrlOffOut(0))
		buf := u.selectedBuffer(0, false)
		half := usbBufferHalf(ctrl, buf)
		if half&usbBufAvail == 0 || half&usbBufFull != 0 {
			return
		}
		payload := make([]byte, req.length)
		u.writeBytes(u.bufferAddr(0, false, buf), payload)
		u.writeEP0OUT(int(req.length), true)
		u.outSent = true
		return
	}
	if req.length > 0 && u.outSent {
		if u.buffStatus&(1<<1) != 0 {
			return // data stage still pending
		}
	}

	// Status stage: the firmware arms an IN zero-length packet.
	if !u.epInReady(0) {
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
			u.chip.Logger.Debug("usb descriptor",
				slog.Int("value", int(req.value)),
				slog.Int("bytes", len(u.collected)),
				slog.String("head", fmt.Sprintf("% x", u.collected[:min(8, len(u.collected))])))
		}
		// The full configuration descriptor (not the 9-byte header) carries the
		// interface classes and endpoints the host must address.
		if req.request == 6 && req.value == 0x0200 && req.length > 9 && len(u.collected) >= 9 {
			u.parseConfig(u.collected)
		}
	}
	u.step++
	if u.step < len(hostEnumeration) {
		u.beginStep()
		return
	}
	u.state = usbHostRunning
	u.chip.Logger.Debug("usb enumeration complete")
}

// parseConfig discovers the CDC and MIDI endpoints from the configuration
// descriptor. TinyGo's CDCMIDI and pico-sdk TinyUSB are free to number their
// interfaces and endpoints differently; the host must follow the descriptor
// rather than assume EP2/EP3.
func (u *usbBlock) parseConfig(desc []byte) {
	u.setEndpointDefaults()
	if len(desc) < 9 || desc[1] != 2 { // CONFIGURATION
		return
	}
	total := int(desc[2]) | int(desc[3])<<8
	total = min(total, len(desc))
	var class, subclass byte
	found := false
	for off := 0; off+2 <= total; {
		blen := int(desc[off])
		if blen < 2 || off+blen > total {
			break
		}
		typ := desc[off+1]
		switch typ {
		case 4: // INTERFACE
			if blen >= 9 {
				class, subclass = desc[off+5], desc[off+6]
			}
		case 5: // ENDPOINT
			if blen >= 7 {
				addr := desc[off+2]
				xfer := desc[off+3] & 0x03
				in := addr&0x80 != 0
				ep := int(addr & 0x0F)
				switch {
				// CDC data interface: bulk IN/OUT.
				case class == 0x0A:
					if in {
						u.cdcIn = ep
					} else {
						u.cdcOut = ep
					}
					found = true
				// CDC communication interface: the interrupt notification EP.
				case class == 0x02 && in && xfer == 3:
					u.cdcNotify = ep
					found = true
				// MIDI streaming interface: bulk IN/OUT.
				case class == 0x01 && subclass == 0x03:
					if in {
						u.midiIn = ep
					} else {
						u.midiOut = ep
					}
					found = true
				}
			}
		}
		off += blen
	}
	if found {
		u.chip.Logger.Debug("usb endpoints",
			slog.Int("cdc_notify", u.cdcNotify),
			slog.Int("cdc_in", u.cdcIn),
			slog.Int("cdc_out", u.cdcOut),
			slog.Int("midi_in", u.midiIn),
			slog.Int("midi_out", u.midiOut))
	}
}

// pollEndpoints drains the IN packets the firmware arms for the descriptors'
// endpoints: CDC data goes to the console, MIDI to the MIDI bridge. CDC
// notification (serial state) packets are acknowledged and ignored.
func (u *usbBlock) pollEndpoints(c *Chip) {
	for ep := range 16 {
		if !u.epInReady(ep) {
			continue
		}
		data := u.takeIN(ep)
		if len(data) == 0 {
			continue
		}
		switch ep {
		case u.cdcIn:
			if u.OnCDC != nil {
				u.OnCDC(data)
			}
		case u.midiIn:
			if u.midi && c.OnUSBMIDI != nil {
				c.OnUSBMIDI(data)
			}
		case u.cdcNotify:
			// Serial-state notification: no host consumer.
		default:
			u.chip.Logger.Debug("usb in packet on unexpected endpoint", slog.Int("ep", ep), slog.Int("bytes", len(data)))
		}
	}
}

// sendOUT publishes one host-to-device packet on an OUT endpoint, toggling the
// DATA0/DATA1 sequence the device expects.
func (u *usbBlock) sendOUT(ep int, data []byte) bool {
	if ep <= 0 || !u.epOutReady(ep) {
		return false
	}
	if len(data) > 64 {
		data = data[:64]
	}
	buf := u.selectedBuffer(ep, false)
	off := usbBufCtrlOffOut(ep)
	ctrl := u.word(off)
	armed := usbBufferHalf(ctrl, buf)
	u.writeBytes(u.bufferAddr(ep, false, buf), data)
	half := uint32(len(data)) | usbBufFull
	if armed&usbBufData1 != 0 {
		half |= usbBufData1
	}
	u.setWord(off, usbSetBufferHalf(ctrl, buf, half))
	u.completeBuffer(ep, false, buf)
	return true
}

// MIDIReady reports whether the firmware has armed the MIDI OUT endpoint for
// a host-to-device packet.
func (u *usbBlock) MIDIReady() bool {
	return u.midi && u.state == usbHostRunning && u.epOutReady(u.midiOut)
}

// SendMIDI queues a host-to-device USB-MIDI packet on the MIDI OUT endpoint.
func (u *usbBlock) SendMIDI(data []byte) bool {
	if !u.MIDIReady() {
		return false
	}
	return u.sendOUT(u.midiOut, data)
}

// CDCReady reports whether the host can publish another CDC OUT packet; the
// device consumes the previous one and clears the buffer control.
func (u *usbBlock) CDCReady() bool {
	return u.state == usbHostRunning && u.epOutReady(u.cdcOut)
}

// SendCDC queues a packet for the host to deliver to the device's CDC OUT
// endpoint. The UI and console input use it.
func (u *usbBlock) SendCDC(data []byte) {
	u.sendOUT(u.cdcOut, data)
}
