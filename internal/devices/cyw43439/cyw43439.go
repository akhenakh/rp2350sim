// Package cyw43439 models the Infineon CYW43439 radio's gSPI host interface.
//
// The model sits below every driver: it observes WL_CS, WL_CLK and the
// bidirectional WL_D pins and speaks the 32-bit gSPI command/status protocol.
// It does not execute Wi-Fi firmware or 802.11; the backplane download is
// accepted and the chip reports "firmware running", then answers SDPCM
// control traffic. Data frames are added by the virtual-AP layer.
package cyw43439

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

var discardLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func loggerOr(l *slog.Logger) *slog.Logger {
	if l == nil {
		return discardLogger
	}
	return l
}

// Config is the board wiring and identity of the radio.
type Config struct {
	RegOn int // WL_REG_ON, usually GP23
	Data  int // WL_D (bidirectional), usually GP24
	CS    int // WL_CS, usually GP25
	CLK   int // WL_CLK, usually GP29
	MAC   [6]byte
	// SSID, when set, is the only network the virtual AP accepts. Password is
	// currently advisory; the association always succeeds.
	SSID     string
	Password string
	// Network configures the virtual AP's IPv4 network and NAT. Nil uses the
	// defaults (10.0.2.15/24, gateway 10.0.2.2, DNS 10.0.2.3).
	Network *NetConfig
	// Logger receives bring-up and unknown-IOCTL diagnostics.
	Logger *slog.Logger
}

// NetConfig is the L3 network behind the virtual AP.
type NetConfig struct {
	GuestIP   [4]byte
	GatewayIP [4]byte
	DNSIP     [4]byte
	Prefix    int
	APMAC     [6]byte
	// Hosts maps a DNS name to an address (IPv4 or IPv6 literal). Names not in
	// the map are resolved by the host resolver.
	Hosts map[string]string
	// Disable turns off the gVisor data path (radio control only).
	Disable bool
}

func (n *NetConfig) defaults() {
	if n.GuestIP == ([4]byte{}) {
		n.GuestIP = [4]byte{10, 0, 2, 15}
	}
	if n.GatewayIP == ([4]byte{}) {
		n.GatewayIP = [4]byte{10, 0, 2, 2}
	}
	if n.DNSIP == ([4]byte{}) {
		n.DNSIP = [4]byte{10, 0, 2, 3}
	}
	if n.Prefix == 0 {
		n.Prefix = 24
	}
	if n.APMAC == ([6]byte{}) {
		n.APMAC = [6]byte{0x02, 0xaa, 0xbb, 0xcc, 0xdd, 0xee}
	}
}

// dataPath is the host-side network behind the radio (gVisor on desktop, a
// no-op stub in the browser build).
type dataPath interface {
	handleFrame([]byte)
	tick(micros uint64)
	close()
}

type bus interface {
	data() bool
	drive(level bool)
	release()
}

type pinBus struct {
	chip *rp2350.Chip
	pin  int
}

func (b pinBus) data() bool   { return b.chip.Pins.Level(b.pin) }
func (b pinBus) drive(v bool) { b.chip.Pins.SetExternal(b.pin, v) }
func (b pinBus) release()     { b.chip.Pins.ReleaseExternal(b.pin) }

type phase int

const (
	phaseIdle phase = iota
	phaseCommand
	phaseWrite
	phaseRead
	phaseStatus
)

// Device is one CYW43439 attached to the board.
type Device struct {
	cfg   Config
	bus   bus
	chip  *rp2350.Chip
	log   *slog.Logger
	debug bool

	regOn   bool
	cs      bool
	clk     bool
	phase   phase
	bit     int
	cmd     uint32
	write   bool
	fn      int
	addr    uint32
	length  int
	word32  bool
	words   int
	data    []byte
	status  uint32
	stBit   int
	version uint64

	// busRegs holds the SPI function-0 registers.
	busRegs map[uint32]uint32
	testReg uint32
	window  uint32
	clock   uint32
	intMask uint32
	water   uint32
	cores   [2]coreState

	firmwareUp bool
	queue      [][]byte
	scheduled  []eventSpec
	eventSeq   uint8
	led        bool
	linkUp     bool
	ssid       string
	fwBytes    int
	hostNet    dataPath
	dataSeq    uint8
	mu         sync.Mutex
}

// eventSpec is one async SDPCM event to queue after the current control
// response has been sent.
type eventSpec struct {
	typ    uint32
	status uint32
	reason uint32
	flags  uint32
}

// Async event types (WLC_E_*).
const (
	evSETSSID = 0
	evJOIN    = 1
	evAUTH    = 3
	evLINK    = 16
	evPSKSUP  = 46
)

type coreState struct {
	ioctrl   uint8
	resetctl uint8
}

const (
	spiTestPattern = 0xFEEDBEAD
	chipID43439    = 0x43439

	// Bus function 0 registers.
	spiBusControl  = 0x00
	spiIntStatus   = 0x04
	spiIntEnable   = 0x06
	spiStatusReg   = 0x08
	spiFunc1Info   = 0x0C
	spiFunc2Info   = 0x0E
	spiFunc3Info   = 0x10
	spiTestReg     = 0x14
	spiRWTestReg   = 0x18
	spiRespDelayF0 = 0x1C
	spiRespDelayF1 = 0x1D
	spiRespDelayF2 = 0x1E
	spiRespDelayF3 = 0x1F

	// Status register bits.
	statusF2Ready     = 1 << 5
	statusF2Available = 1 << 8
	statusF2LenShift  = 9

	// Raw backplane (SDIO core) registers.
	sdioWindowLow  = 0x1000A
	sdioWindowMid  = 0x1000B
	sdioWindowHigh = 0x1000C
	sdioClockCSR   = 0x1000E
	sdioPullUp     = 0x1000F
	sdioF2Water    = 0x10008
	chipCommonBase = 0x18000000
	wlanCoreBase   = 0x18103000
	socramCoreBase = 0x18104000
	coreIOCtrl     = 0x408
	coreResetCtrl  = 0x800
	coreSICFClock  = 0x01
	coreAIRCReset  = 0x01

	// SDPCM/CDC constants.
	sdpcmHeaderLen = 12
	cdcHeaderLen   = 16
	wlcGetVar      = 262
	wlcSetVar      = 263
	wlcSetSSID     = 26
	channelControl = 0
	maxQueue       = 64 // F2 packets waiting for the guest
)

func swap16(v uint32) uint32 { return v>>16 | v<<16 }

// Attach builds the gSPI model and connects it to the board pins.
func Attach(c *rp2350.Chip, cfg Config) *Device {
	if cfg.MAC == ([6]byte{}) {
		cfg.MAC = [6]byte{0x02, 0x11, 0x22, 0x33, 0x44, 0x55}
	}
	d := newDevice(pinBus{chip: c, pin: cfg.Data}, cfg)
	d.chip = c
	c.Pins.AddChangeListener(d.pinChange)
	c.AddResetHook(func() {
		d.reset()
		d.enabledFromPins()
	})
	d.reset()
	d.enabledFromPins()
	if cfg.Network == nil {
		cfg.Network = &NetConfig{}
	}
	cfg.Network.defaults()
	d.cfg.Network = cfg.Network
	if !cfg.Network.Disable {
		if dp, err := newDataPath(d); err != nil {
			d.log.Error("cyw43439 network", slog.Any("err", err))
		} else {
			d.hostNet = dp
			// gVisor timers run on virtual time; the hook also drains packets
			// the host stack produces between guest transactions.
			c.AddPeriodicHook(100*rp2350.CyclesPerMicrosecond, d.tickNetwork)
		}
	}
	return d
}

func (d *Device) tickNetwork() {
	if d.hostNet != nil && d.chip != nil {
		d.hostNet.tick(d.chip.NowMicros())
	}
}

func newDevice(b bus, cfg Config) *Device {
	d := &Device{
		cfg:     cfg,
		bus:     b,
		log:     loggerOr(cfg.Logger),
		busRegs: map[uint32]uint32{},
	}
	d.debug = d.log.Handler().Enabled(context.Background(), slog.LevelDebug)
	d.reset()
	return d
}

func (d *Device) reset() {
	d.phase = phaseIdle
	d.bit = 0
	d.cmd = 0
	d.words = 0
	d.data = nil
	d.status = 0
	d.stBit = 0
	d.word32 = false
	d.busRegs = map[uint32]uint32{}
	d.testReg = 0
	d.window = 0
	d.clock = 0
	d.intMask = 0
	d.water = 0
	d.cores = [2]coreState{}
	d.firmwareUp = false
	d.queue = nil
	d.scheduled = nil
	d.eventSeq = 0
	d.led = false
	d.linkUp = false
	d.ssid = ""
	d.fwBytes = 0
	d.dataSeq = 0
	d.version++
	d.bus.drive(true)
}

func (d *Device) enabledFromPins() {
	if d.chip == nil {
		d.regOn = true
		return
	}
	d.regOn = d.chip.Pins.Level(d.cfg.RegOn)
	d.cs = !d.chip.Pins.Level(d.cfg.CS)
	d.clk = d.chip.Pins.Level(d.cfg.CLK)
}

func (d *Device) pinChange(pin int, level bool) {
	switch pin {
	case d.cfg.RegOn:
		d.regOn = level
		d.reset()
	case d.cfg.CS:
		d.setCS(level)
	case d.cfg.CLK:
		d.setCLK(level)
	}
}

func (d *Device) setCS(level bool) {
	selected := !level
	if selected == d.cs {
		return
	}
	d.cs = selected
	if !selected {
		d.phase = phaseIdle
		d.bus.drive(true)
		return
	}
	d.phase = phaseCommand
	d.bit = 0
	d.cmd = 0
	d.write = false
	d.words = 0
	d.data = nil
	d.stBit = 0
	d.bus.release()
}

func (d *Device) setCLK(level bool) {
	if !d.cs {
		d.clk = level
		return
	}
	if !level {
		if d.phase == phaseRead || d.phase == phaseStatus {
			d.bus.drive(d.bitToDrive())
		}
		d.clk = level
		return
	}
	if d.clk {
		return
	}
	d.clk = level
	switch d.phase {
	case phaseCommand:
		d.sampleCommand()
	case phaseWrite:
		d.sampleWrite()
	case phaseRead:
		d.bit++
		if d.bit == d.words*32 {
			d.phase = phaseStatus
			d.stBit = 0
		}
	case phaseStatus:
		d.stBit++
		if d.stBit == 32 {
			d.phase = phaseIdle
			d.bus.drive(true)
		}
	}
}

func (d *Device) bitToDrive() bool {
	switch d.phase {
	case phaseRead:
		// gSPI words travel MSB first; the PIO shifts left on IN.
		if d.bit < len(d.data)*8 {
			word := d.bit / 32
			pos := d.bit % 32
			return d.data[word*4+3-pos/8]&(1<<uint(7-pos%8)) != 0
		}
	case phaseStatus:
		if d.stBit < 32 {
			return d.status&(1<<uint(31-d.stBit)) != 0
		}
	}
	return false
}

func (d *Device) sampleCommand() {
	if d.bus.data() {
		d.cmd = d.cmd<<1 | 1
	} else {
		d.cmd <<= 1
	}
	d.bit++
	if d.bit < 32 {
		return
	}
	cmd := d.cmd
	if !d.word32 {
		cmd = swap16(cmd)
	}
	d.write = cmd&(1<<31) != 0
	d.fn = int(cmd >> 28 & 3)
	d.addr = cmd >> 11 & 0x1ffff
	d.length = int(cmd & 0x7ff)
	if d.debug {
		d.log.Debug("cyw43439 command",
			slog.Bool("write", d.write), slog.Int("fn", d.fn),
			slog.String("addr", fmt.Sprintf("%#x", d.addr)), slog.Int("len", d.length),
			slog.String("raw", fmt.Sprintf("%#x", d.cmd)), slog.Bool("w32", d.word32),
			slog.String("window", fmt.Sprintf("%#x", d.window)))
	}
	if d.write {
		d.words = d.writeWords()
		d.data = make([]byte, d.words*4)
		d.bit = 0
		d.phase = phaseWrite
		return
	}
	d.data = d.readResponse()
	if !d.word32 {
		for i := 0; i+4 <= len(d.data); i += 4 {
			v := binary.LittleEndian.Uint32(d.data[i:])
			binary.LittleEndian.PutUint32(d.data[i:], swap16(v))
		}
	}
	if len(d.data)%4 != 0 {
		d.data = append(d.data, make([]byte, 4-len(d.data)%4)...)
	}
	d.words = len(d.data) / 4
	d.status = d.statusWord()
	d.bit = 0
	d.phase = phaseRead
}

func (d *Device) writeWords() int {
	switch d.fn {
	case 0:
		return 1
	case 1:
		n := (d.length + 3) / 4
		if n == 0 {
			n = 1
		}
		// bp_write's command includes one response-delay word; writen() does
		// not. Small backplane register writes use writen().
		if d.length > 4 {
			n++
		}
		return n
	default:
		n := (d.length + 3) / 4
		if n == 0 {
			n = 1
		}
		return n
	}
}

func (d *Device) sampleWrite() {
	if d.bit < d.words*32 {
		word := d.bit / 32
		pos := d.bit % 32
		if d.bus.data() {
			d.data[word*4+3-pos/8] |= 1 << uint(7-pos%8)
		}
	}
	d.bit++
	if d.bit < d.words*32 {
		return
	}
	data := d.data
	if !d.word32 {
		for i := 0; i+4 <= len(data); i += 4 {
			v := binary.LittleEndian.Uint32(data[i:])
			binary.LittleEndian.PutUint32(data[i:], swap16(v))
		}
	}
	d.applyWrite(data)
	d.status = d.statusWord()
	d.phase = phaseStatus
	d.stBit = 0
}

func (d *Device) applyWrite(data []byte) {
	if d.length <= 0 || d.length > len(data) {
		return
	}
	data = data[:d.length]
	switch d.fn {
	case 0:
		d.busWrite(d.addr, data)
	case 1:
		if d.debug {
			d.log.Debug("cyw43439 bp write", slog.String("addr", fmt.Sprintf("%#x", d.fullAddr(d.addr))),
				slog.String("data", fmt.Sprintf("% x", data)))
		}
		d.bpWrite(d.fullAddr(d.addr), data)
	case 2, 3:
		d.wlanWrite(data)
	}
}

func (d *Device) readResponse() []byte {
	switch d.fn {
	case 0:
		return d.busRead(d.addr, d.length)
	case 1:
		payload := d.bpRead(d.fullAddr(d.addr), d.length)
		// The F1 read path adds one response-delay word before the data.
		out := make([]byte, 4+len(payload))
		copy(out[4:], payload)
		return out
	case 2:
		return d.wlanRead(d.length)
	}
	return nil
}

func (d *Device) statusWord() uint32 {
	d.mu.Lock()
	defer d.mu.Unlock()
	var s uint32
	if d.firmwareUp {
		s |= statusF2Ready
	}
	if len(d.queue) > 0 {
		s |= statusF2Available | ((uint32(len(d.queue[0])) & 0x7ff) << statusF2LenShift)
	}
	return s
}

// ---- function 0: SPI registers ----

func (d *Device) busRead(addr uint32, length int) []byte {
	var v uint32
	switch addr {
	case spiBusControl:
		v = d.busRegs[addr]
	case spiIntStatus, spiIntEnable:
		v = d.busRegs[addr]
	case spiStatusReg:
		v = d.statusWord()
	case spiFunc1Info, spiFunc2Info, spiFunc3Info:
		v = 3 // enabled + ready
	case spiTestReg:
		v = spiTestPattern
	case spiRWTestReg:
		v = d.testReg
	case spiRespDelayF0, spiRespDelayF1, spiRespDelayF2, spiRespDelayF3:
		v = d.busRegs[addr]
	default:
		v = d.busRegs[addr]
	}
	return leBytes(v, length)
}

func (d *Device) busWrite(addr uint32, data []byte) {
	v := leValue(data)
	switch addr {
	case spiBusControl:
		d.busRegs[addr] = v
		d.word32 = v&1 != 0
	case spiRWTestReg:
		d.testReg = v
	case spiIntStatus, spiIntEnable, spiRespDelayF0, spiRespDelayF1, spiRespDelayF2, spiRespDelayF3:
		d.busRegs[addr] = v
	}
}

// ---- function 1: backplane ----

func (d *Device) fullAddr(raw uint32) uint32 {
	if raw > 0x7fff {
		return raw
	}
	return d.window&^0x7fff | raw&0x7fff
}

func (d *Device) bpRead(full uint32, length int) []byte {
	var v uint32
	switch {
	case full == chipCommonBase:
		v = chipID43439
	case full == sdioClockCSR:
		v = d.clock
	case full == sdioPullUp:
		v = 0
	case full == sdioF2Water:
		v = d.water
	case full == sdioWindowLow:
		v = d.window >> 8 & 0xff
	case full == sdioWindowMid:
		v = d.window >> 16 & 0xff
	case full == sdioWindowHigh:
		v = d.window >> 24 & 0xff
	case full == 0x18002024:
		v = d.intMask
	case full >= wlanCoreBase && full < wlanCoreBase+0x1000:
		v = d.coreRead(0, full-wlanCoreBase)
	case full >= socramCoreBase && full < socramCoreBase+0x1000:
		v = d.coreRead(1, full-socramCoreBase)
	}
	return leBytes(v, length)
}

func (d *Device) coreRead(core int, off uint32) uint32 {
	switch off {
	case coreIOCtrl:
		return uint32(d.cores[core].ioctrl)
	case coreResetCtrl:
		return uint32(d.cores[core].resetctl)
	}
	return 0
}

func (d *Device) bpWrite(full uint32, data []byte) {
	v := leValue(data)
	switch full {
	case sdioWindowLow:
		d.window = d.window&0xffff00ff | (v&0xff)<<8
	case sdioWindowMid:
		d.window = d.window&0xff00ffff | (v&0xff)<<16
	case sdioWindowHigh:
		d.window = d.window&0x00ffffff | (v&0xff)<<24
	case sdioClockCSR:
		d.clock = 0
		if v&0x08 != 0 {
			d.clock |= 0x40 // ALP available
		}
		if v&0x10 != 0 {
			d.clock |= 0x80 // HT available
		}
	case sdioPullUp:
		// No pull configuration in the model.
	case sdioF2Water:
		d.water = v
	case 0x18002024:
		d.intMask = v
	default:
		if full >= wlanCoreBase && full < wlanCoreBase+0x1000 {
			d.coreWrite(0, full-wlanCoreBase, v)
		} else if full >= socramCoreBase && full < socramCoreBase+0x1000 {
			d.coreWrite(1, full-socramCoreBase, v)
		} else if full < 0x80000 {
			// The driver downloads the firmware and NVRAM into SOCRAM/ATCM.
			d.fwBytes += len(data)
		}
	}
}

func (d *Device) coreWrite(core int, off, v uint32) {
	switch off {
	case coreIOCtrl:
		d.cores[core].ioctrl = uint8(v)
	case coreResetCtrl:
		d.cores[core].resetctl = uint8(v)
		if core == 0 && v&coreAIRCReset == 0 && d.cores[0].ioctrl&coreSICFClock != 0 {
			d.mu.Lock()
			d.firmwareUp = true
			d.mu.Unlock()
			// The running firmware brings the HT clock up by itself; the
			// driver waits for SBSDIO_HT_AVAIL before it explicitly requests
			// it later in the init sequence.
			d.clock |= 0x80
			d.log.Debug("cyw43439 firmware running", slog.Int("downloaded", d.fwBytes))
		}
	}
}

// ---- function 2: WLAN/control ----

func (d *Device) wlanWrite(packet []byte) {
	if len(packet) < sdpcmHeaderLen+cdcHeaderLen {
		return
	}
	size := int(binary.LittleEndian.Uint16(packet))
	if size < sdpcmHeaderLen+cdcHeaderLen || size > len(packet) {
		return
	}
	hdrLen := int(packet[7])
	if hdrLen < sdpcmHeaderLen || hdrLen+cdcHeaderLen > size {
		return
	}
	channel := packet[5] & 0xf
	if channel == 2 {
		d.handleDataFrame(packet, hdrLen, size)
		return
	}
	if channel != channelControl {
		return
	}
	seq := packet[4]
	cdc := packet[hdrLen:]
	cmd := binary.LittleEndian.Uint32(cdc)
	clen := int(binary.LittleEndian.Uint32(cdc[4:]))
	flags := binary.LittleEndian.Uint16(cdc[8:])
	id := binary.LittleEndian.Uint16(cdc[10:])
	if clen < 0 || hdrLen+cdcHeaderLen+clen > size {
		return
	}
	payload := packet[hdrLen+cdcHeaderLen : hdrLen+cdcHeaderLen+clen]
	resp := d.ioctl(cmd, flags&3, payload)
	d.queueResponse(seq, cmd, flags&3, id, resp)
	// Events follow the control response so the driver's ioctl wait finds its
	// answer first and the next poll drains them in order.
	for _, ev := range d.scheduled {
		d.queueEvent(ev)
	}
	d.scheduled = nil
}

func (d *Device) scheduleEvents(events ...eventSpec) {
	d.scheduled = append(d.scheduled, events...)
}

// handleDataFrame strips the BDC header from an SDPCM data packet and hands
// the Ethernet frame to the host data path.
func (d *Device) handleDataFrame(packet []byte, hdrLen, size int) {
	if d.hostNet == nil || hdrLen+4 > size {
		return
	}
	off := hdrLen + 4 + 4*int(packet[hdrLen+3])
	if off > size {
		return
	}
	d.hostNet.handleFrame(packet[off:size])
}
func (d *Device) queueDataFrame(frame []byte) {
	const bdcLen = 4
	if d.hostNet == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.linkUp {
		return
	}
	if len(d.queue) >= maxQueue {
		if d.debug {
			d.log.Debug("cyw43439 data drop", slog.Int("queued", len(d.queue)))
		}
		return
	}
	total := sdpcmHeaderLen + bdcLen + len(frame)
	pkt := make([]byte, total)
	binary.LittleEndian.PutUint16(pkt, uint16(total))
	binary.LittleEndian.PutUint16(pkt[2:], ^uint16(total))
	pkt[4] = d.dataSeq
	d.dataSeq++
	pkt[5] = 2 // DATA channel
	pkt[7] = sdpcmHeaderLen
	pkt[9] = d.dataSeq + 8
	pkt[sdpcmHeaderLen] = 2 << 4 // BDC version
	copy(pkt[sdpcmHeaderLen+bdcLen:], frame)
	d.queue = append(d.queue, pkt)
	d.version++
}

// queueEvent builds one SDPCM async-event packet: BDC header, the 0x886c
// Ethernet frame, the Broadcom event header and the 48-byte event message.
func (d *Device) queueEvent(ev eventSpec) {
	const (
		bdcLen   = 4
		payload  = 72
		total    = sdpcmHeaderLen + bdcLen + payload
		frameOff = sdpcmHeaderLen + bdcLen
		hdrOff   = frameOff + 14
		msgOff   = hdrOff + 10
	)
	pkt := make([]byte, total)
	binary.LittleEndian.PutUint16(pkt, uint16(total))
	binary.LittleEndian.PutUint16(pkt[2:], ^uint16(total))
	pkt[4] = d.eventSeq
	d.eventSeq++
	pkt[5] = 1 // ASYNCEVENT channel
	pkt[7] = sdpcmHeaderLen
	pkt[9] = 8 // bus data credit
	// Ethernet frame carrying the Broadcom event.
	copy(pkt[frameOff:], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	copy(pkt[frameOff+6:], d.cfg.MAC[:])
	pkt[frameOff+12] = 0x88
	pkt[frameOff+13] = 0x6c
	binary.BigEndian.PutUint16(pkt[hdrOff:], 32769) // BCMILCP_SUBTYPE_VENDOR_LONG
	binary.BigEndian.PutUint16(pkt[hdrOff+2:], uint16(payload-24))
	pkt[hdrOff+4] = 0
	copy(pkt[hdrOff+5:], []byte{0x00, 0x10, 0x18})
	binary.BigEndian.PutUint16(pkt[hdrOff+8:], 1) // BCM subtype event
	binary.BigEndian.PutUint16(pkt[msgOff:], 0)   // version
	binary.BigEndian.PutUint16(pkt[msgOff+2:], uint16(ev.flags))
	binary.BigEndian.PutUint32(pkt[msgOff+4:], ev.typ)
	binary.BigEndian.PutUint32(pkt[msgOff+8:], ev.status)
	binary.BigEndian.PutUint32(pkt[msgOff+12:], ev.reason)
	copy(pkt[msgOff+24:], d.cfg.MAC[:]) // address
	copy(pkt[msgOff+30:], "wl0")
	d.mu.Lock()
	d.queue = append(d.queue, pkt)
	d.version++
	d.mu.Unlock()
}

func (d *Device) queueResponse(seq uint8, cmd uint32, kind uint16, id uint16, payload []byte) {
	total := sdpcmHeaderLen + cdcHeaderLen + len(payload)
	pkt := make([]byte, total)
	binary.LittleEndian.PutUint16(pkt, uint16(total))
	binary.LittleEndian.PutUint16(pkt[2:], ^uint16(total))
	pkt[4] = seq
	pkt[5] = channelControl
	pkt[7] = sdpcmHeaderLen
	pkt[9] = seq + 8 // bus data credit
	binary.LittleEndian.PutUint32(pkt[sdpcmHeaderLen:], cmd)
	binary.LittleEndian.PutUint32(pkt[sdpcmHeaderLen+4:], uint32(len(payload)))
	binary.LittleEndian.PutUint16(pkt[sdpcmHeaderLen+8:], kind)
	binary.LittleEndian.PutUint16(pkt[sdpcmHeaderLen+10:], id)
	copy(pkt[sdpcmHeaderLen+cdcHeaderLen:], payload)
	d.mu.Lock()
	d.queue = append(d.queue, pkt)
	d.version++
	d.mu.Unlock()
}

func (d *Device) wlanRead(length int) []byte {
	if length <= 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.queue) == 0 {
		return nil
	}
	pkt := d.queue[0]
	d.queue = d.queue[1:]
	d.version++
	if len(pkt) > length {
		pkt = pkt[:length]
	}
	return pkt
}

// ioctl answers one CDC request. kind is 0 for GET and 2 for SET.
func (d *Device) ioctl(cmd uint32, kind uint16, payload []byte) []byte {
	switch cmd {
	case wlcSetVar:
		name, value := splitIovar(payload)
		d.setIovar(name, value, len(payload))
		return nil
	case wlcGetVar:
		name, _ := splitIovar(payload)
		return d.getIovar(name, len(payload))
	case wlcSetSSID:
		d.joinRequest(payload)
		return nil
	default:
		// Direct WLC_* ioctls (WLC_UP and friends) are acknowledged. A GET of
		// one returns a zero word.
		if kind == 0 {
			return make([]byte, 4)
		}
		return nil
	}
}

func splitIovar(payload []byte) (string, []byte) {
	for i, b := range payload {
		if b == 0 {
			return string(payload[:i]), payload[i+1:]
		}
	}
	return string(payload), nil
}

// joinRequest records the SSID and queues the association events. The payload
// starts with the SSID length, followed by the SSID bytes.
func (d *Device) joinRequest(payload []byte) {
	ssid := d.ssid
	if len(payload) >= 4 {
		n := min(int(binary.LittleEndian.Uint32(payload)), 32)
		if len(payload) >= 4+n {
			ssid = string(payload[4 : 4+n])
		}
	}
	d.log.Debug("cyw43439 join", slog.String("ssid", ssid))
	if d.cfg.SSID != "" && ssid != d.cfg.SSID {
		d.log.Debug("cyw43439 join rejected", slog.String("want", d.cfg.SSID))
		d.mu.Lock()
		d.ssid = ssid
		d.linkUp = false
		d.version++
		d.mu.Unlock()
		d.scheduleEvents(eventSpec{typ: evSETSSID, status: 3}) // no matching network
		return
	}
	d.mu.Lock()
	d.ssid = ssid
	d.linkUp = true
	d.version++
	d.mu.Unlock()
	d.scheduleEvents(
		eventSpec{typ: evSETSSID, status: 0},
		eventSpec{typ: evAUTH, status: 0},
		eventSpec{typ: evJOIN, status: 0},
		eventSpec{typ: evPSKSUP, status: 6},
	)
}

func (d *Device) getIovar(name string, reqLen int) []byte {
	switch name {
	case "cur_etheraddr":
		out := make([]byte, reqLen)
		copy(out, d.cfg.MAC[:])
		return out
	case "ver":
		return []byte("7.95.61\x00")
	default:
		return make([]byte, reqLen)
	}
}

func (d *Device) setIovar(name string, value []byte, reqLen int) {
	switch name {
	case "gpioout":
		if len(value) >= 8 {
			mask := binary.LittleEndian.Uint32(value)
			val := binary.LittleEndian.Uint32(value[4:])
			if mask&1 != 0 {
				d.mu.Lock()
				d.led = val&1 != 0
				d.version++
				d.mu.Unlock()
			}
		}
	case "bsscfg:ssid":
		d.joinRequest(value)
	default:
		if d.debug {
			d.log.Debug("cyw43439 iovar", slog.String("name", name), slog.Int("len", reqLen))
		}
	}
}

// ---- host-facing state ----

// LED reports WL_GPIO0, the Pico W's on-board LED.
func (d *Device) LED() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.led
}

// FirmwareUp reports whether the driver has downloaded firmware and released
// the WLAN core.
func (d *Device) FirmwareUp() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.firmwareUp
}

// LinkUp reports whether a join completed.
func (d *Device) LinkUp() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.linkUp
}

// SSID reports the network the station joined.
func (d *Device) SSID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ssid
}

// Version changes whenever the visible state changes.
func (d *Device) Version() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.version
}

// Close releases the pin drive.
func (d *Device) Close() { d.bus.release() }

func leBytes(v uint32, n int) []byte {
	out := make([]byte, n)
	for i := 0; i < n && i < 4; i++ {
		out[i] = byte(v >> (8 * i))
	}
	return out
}

func leValue(data []byte) uint32 {
	var v uint32
	for i := 0; i < len(data) && i < 4; i++ {
		v |= uint32(data[i]) << (8 * i)
	}
	return v
}

func (d *Device) String() string {
	return fmt.Sprintf("cyw43439(fw=%v led=%v queue=%d)", d.firmwareUp, d.led, len(d.queue))
}
