package ble

import (
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"

	"github.com/akhenakh/rp2350sim/internal/m33"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

const (
	// attCID is the fixed L2CAP channel for ATT.
	attCID uint16 = 0x0004
	// connHandle is the first dynamic LE connection handle.
	connHandle uint16 = 0x0040
	// defaultMTU is the initial ATT MTU; the peer and host negotiate up.
	defaultMTU uint16 = 23
	// defaultInterval is 30 ms in 1.25 ms units.
	defaultInterval uint16 = 24
)

// Options configures the virtual controller.
type Options struct {
	// HostWake is the CYW43 HOST_WAKE GPIO. The controller drives it low while
	// an HCI event is queued, matching the radio's falling-edge interrupt.
	HostWake int
	// Address is the advertised peer address in display order
	// (C0:DE:12:34:56:78).
	Address [6]byte
	// Seed seeds the controller's private PRNG. The global math/rand source is
	// never used, so two runs with the same firmware and seed are identical.
	Seed int64
}

// Controller is the logical BLE controller behind the CYW43 HCI transport.
type Controller struct {
	chip     *rp2350.Chip
	hostWake int
	addr     [6]byte
	hostAddr [6]byte
	rng      *rand.Rand

	events   [][]byte
	wakeLow  bool
	hciLog   *PCAPNG
	observer func(dir string, pkt []byte)

	peer     *peer
	conn     *connection
	scanning bool
	advOn    bool
	advData  []byte

	scanDeadline       *rp2350.Deadline
	connectDeadline    *rp2350.Deadline
	disconnectDeadline *rp2350.Deadline
	notifyDeadline     *rp2350.Deadline
	updateDeadline     *rp2350.Deadline
	featuresDeadline   *rp2350.Deadline

	notifyLeft int
}

type connection struct {
	handle   uint16
	peerAddr [6]byte
	peerType byte
	mtu      uint16
	l2buf    []byte
}

// New creates a controller and registers its deadlines. Call BindCYW43 to
// install the firmware symbol hooks.
func New(c *rp2350.Chip, opts Options) *Controller {
	ctrl := &Controller{
		chip:     c,
		hostWake: opts.HostWake,
		addr:     opts.Address,
		peer:     newPeer(),
		rng:      rand.New(rand.NewSource(opts.Seed)),
	}
	// The host's own address is only cosmetic; derive it deterministically
	// from the peer address so Read BD_ADDR is stable.
	ctrl.hostAddr = opts.Address
	ctrl.hostAddr[5] ^= 0x5A

	ctrl.scanDeadline = c.AddDeadline(ctrl.scanTick)
	ctrl.connectDeadline = c.AddDeadline(ctrl.connectComplete)
	ctrl.disconnectDeadline = c.AddDeadline(ctrl.disconnectComplete)
	ctrl.notifyDeadline = c.AddDeadline(ctrl.notifyTick)
	ctrl.updateDeadline = c.AddDeadline(ctrl.updateComplete)
	ctrl.featuresDeadline = c.AddDeadline(ctrl.remoteFeaturesComplete)
	c.AddResetHook(ctrl.Reset)
	return ctrl
}

// Reset clears protocol state when the chip resets. Symbol hooks and deadline
// registrations stay installed.
func (c *Controller) Reset() {
	c.conn = nil
	c.scanning = false
	c.advOn = false
	c.notifyLeft = 0
	c.peer = newPeer()
	c.events = nil
	for _, d := range []*rp2350.Deadline{
		c.scanDeadline, c.connectDeadline, c.disconnectDeadline,
		c.notifyDeadline, c.updateDeadline, c.featuresDeadline,
	} {
		if d != nil {
			d.Cancel()
		}
	}
	c.updateWake()
}

// Status is a snapshot for the UI and summaries.
type Status struct {
	Address       [6]byte
	Connected     bool
	Peer          [6]byte
	MTU           uint16
	Notifications bool
	Pending       int
}

// Status reports the current controller state.
func (c *Controller) Status() Status {
	s := Status{Address: c.addr, Pending: len(c.events)}
	if c.conn != nil {
		s.Connected = true
		s.Peer = c.conn.peerAddr
		s.MTU = c.conn.mtu
		s.Notifications = c.peer.notify
	}
	return s
}

// SetObserver receives every HCI packet as it crosses the transport. dir is
// ">" for host-to-controller and "<" for controller-to-host.
func (c *Controller) SetObserver(fn func(dir string, pkt []byte)) {
	c.observer = fn
}

// Queue returns the number of pending HCI packets.
func (c *Controller) QueueLen() int { return len(c.events) }

// popEvent removes the first queued HCI packet.
// WriteHCI feeds one HCI packet into the controller. packetType is the HCI
// packet indicator (1 command, 2 ACL) and data is the packet without it.
func (c *Controller) WriteHCI(packetType byte, data []byte) { c.writeHCI(packetType, data) }

// PopEvent removes and returns the first queued HCI packet. Byte 0 is the
// packet type and the rest is the HCI payload.
func (c *Controller) PopEvent() []byte { return c.popEvent() }

// PeekEvent returns the first queued HCI packet without removing it, so a
// transport can size it before claiming ring space.
func (c *Controller) PeekEvent() []byte {
	if len(c.events) == 0 {
		return nil
	}
	return c.events[0]
}

// HasEvents reports whether an HCI event or ACL packet is queued.
func (c *Controller) HasEvents() bool { return len(c.events) > 0 }

func (c *Controller) popEvent() []byte {
	if len(c.events) == 0 {
		return nil
	}
	pkt := c.events[0]
	c.events = c.events[1:]
	c.updateWake()
	return pkt
}

func (c *Controller) queue(pkt []byte) {
	if len(pkt) == 0 {
		return
	}
	c.logHCI("<", pkt)
	c.events = append(c.events, pkt)
	c.updateWake()
}

func (c *Controller) updateWake() {
	if c.hostWake < 0 || c.hostWake > 47 {
		return
	}
	wantLow := len(c.events) > 0
	if wantLow == c.wakeLow {
		return
	}
	c.wakeLow = wantLow
	// HOST_WAKE is active low.
	c.chip.Pins.SetExternal(c.hostWake, !wantLow)
}

// busInit replaces cyw43_ll_bus_init: no chip, no firmware download.
func (c *Controller) busInit(cpu *m33.CPU) {
	cpu.R[0] = 0
	cpu.R[15] = cpu.R[14] &^ 1
}

// busSleep replaces cyw43_ll_bus_sleep.
func (c *Controller) busSleep(cpu *m33.CPU) {
	cpu.R[15] = cpu.R[14] &^ 1
}

// wifiGetMAC replaces cyw43_ll_wifi_get_mac: the virtual chip has no OTP MAC,
// so return the board address instead of touching the backplane.
func (c *Controller) wifiGetMAC(cpu *m33.CPU) {
	ptr := cpu.R[1]
	for i := range 6 {
		c.chip.Write8(ptr+uint32(i), c.hostAddr[i])
	}
	cpu.R[0] = 0
	cpu.R[15] = cpu.R[14] &^ 1
}

// btbusInit replaces cyw43_btbus_init.
func (c *Controller) btbusInit(cpu *m33.CPU) {
	cpu.R[0] = 0
	cpu.R[15] = cpu.R[14] &^ 1
}

// btbusWrite replaces cyw43_btbus_write(buf, size). buf points at the four-byte
// CYW43 header, buf[3] is the HCI packet type and the HCI packet starts at
// buf+4.
func (c *Controller) btbusWrite(cpu *m33.CPU) {
	buf := cpu.R[0]
	size := int(cpu.R[1])
	if size < 4 {
		cpu.R[0] = 1
		cpu.R[15] = cpu.R[14] &^ 1
		return
	}
	data, ok := c.chip.PeekBytes(buf, size)
	cpu.R[0] = 0
	cpu.R[15] = cpu.R[14] &^ 1
	if !ok {
		cpu.R[0] = 1
		return
	}
	c.writeHCI(data[3], data[4:])
}

// btbusRead replaces cyw43_btbus_read(buf, max, len_ptr). The transport expects
// buf[3] to hold the packet type and the HCI packet at buf+4; *len includes the
// four-byte header.
func (c *Controller) btbusRead(cpu *m33.CPU) {
	buf := cpu.R[0]
	max := int(cpu.R[1])
	lenPtr := cpu.R[2]
	pkt := c.popEvent()
	if pkt == nil {
		if lenPtr != 0 {
			c.chip.Write32(lenPtr, 0)
		}
		cpu.R[0] = 0
		cpu.R[15] = cpu.R[14] &^ 1
		return
	}
	total := 4 + len(pkt) - 1
	if total > max {
		// Leave the packet queued; the transport's buffers are always large
		// enough for controller events, so this is a host/firmware bug.
		c.events = append([][]byte{pkt}, c.events...)
		if lenPtr != 0 {
			c.chip.Write32(lenPtr, 0)
		}
		cpu.R[0] = 1
		cpu.R[15] = cpu.R[14] &^ 1
		return
	}
	c.chip.Write8(buf, 0)
	c.chip.Write8(buf+1, 0)
	c.chip.Write8(buf+2, 0)
	c.chip.Write8(buf+3, pkt[0])
	for i := 1; i < len(pkt); i++ {
		c.chip.Write8(buf+4+uint32(i-1), pkt[i])
	}
	if lenPtr != 0 {
		c.chip.Write32(lenPtr, uint32(total))
	}
	cpu.R[0] = 0
	cpu.R[15] = cpu.R[14] &^ 1
}

// btHasWork replaces cyw43_ll_bt_has_work: report queued controller events.
func (c *Controller) btHasWork(cpu *m33.CPU) {
	if len(c.events) > 0 {
		cpu.R[0] = 1
	} else {
		cpu.R[0] = 0
	}
	cpu.R[15] = cpu.R[14] &^ 1
}

// wifiHasWork replaces cyw43_ll_has_work: the virtual chip never has Wi-Fi
// work, so the poll path cannot touch an unmodelled backplane.
func (c *Controller) wifiHasWork(cpu *m33.CPU) {
	cpu.R[0] = 0
	cpu.R[15] = cpu.R[14] &^ 1
}

// gpioSet replaces cyw43_gpio_set: the LED is cosmetic.
func (c *Controller) gpioSet(cpu *m33.CPU) {
	cpu.R[0] = 0
	cpu.R[15] = cpu.R[14] &^ 1
}

// SetHCILog starts writing host/controller HCI traffic as Bluetooth HCI H4 in
// a pcapng file.
func (c *Controller) SetHCILog(w io.Writer) error {
	p, err := NewPCAPNG(w)
	if err != nil {
		return err
	}
	c.hciLog = p
	return nil
}

func (c *Controller) logHCI(dir string, pkt []byte) {
	if len(pkt) == 0 {
		return
	}
	if c.observer != nil {
		c.observer(dir, pkt)
	}
	if c.hciLog != nil {
		_ = c.hciLog.Packet(c.chip.Cycles/rp2350.CyclesPerMicrosecond, pkt)
	}
}

func (c *Controller) writeHCI(packetType byte, data []byte) {
	pkt := make([]byte, 1+len(data))
	pkt[0] = packetType
	copy(pkt[1:], data)
	c.logHCI(">", pkt)
	switch packetType {
	case packetCommand:
		if len(data) < 3 {
			return
		}
		opcode := binary.LittleEndian.Uint16(data[0:])
		c.command(opcode, data[3:])
	case packetACL:
		c.acl(data)
	}
}

func (c *Controller) command(opcode uint16, p []byte) {
	switch opcode {
	case opReset:
		c.resetForCommand()
		c.complete(opcode, statusSuccess)
	case opReadLocalVersion:
		c.complete(opcode, statusSuccess,
			8, 0x00, 0x00, // HCI 4.2
			8,          // LMP
			0x0F, 0x00, // Broadcom
			0x00, 0x00)
	case opReadLocalSupportedCommands:
		c.complete(opcode, append([]byte{statusSuccess}, supportedCommands()...)...)
	case opReadLocalSupportedFeatures:
		c.complete(opcode, append([]byte{statusSuccess}, localFeatures()...)...)
	case opReadBufferSize:
		c.complete(opcode, statusSuccess,
			251, 0, // ACL packet length
			0,    // SCO
			3, 0, // ACL packets
			0, 0) // SCO packets
	case opReadBDAddr:
		addr := reverse6(c.hostAddr[:])
		c.complete(opcode, append([]byte{statusSuccess}, addr[:]...)...)
	case opSetControllerToHostFlow:
		c.complete(opcode, statusSuccess)
	case opHostBufferSize:
		c.complete(opcode, statusSuccess)
	case opSetEventMask:
		c.complete(opcode, statusSuccess)
	case opWriteLEHostSupported:
		c.complete(opcode, statusSuccess)
	case opLESetEventMask:
		c.complete(opcode, statusSuccess)
	case opLEReadBufferSize:
		c.complete(opcode, statusSuccess, 251, 0, 3, 0)
	case opLEReadLocalFeatures:
		c.complete(opcode, append([]byte{statusSuccess}, make([]byte, 8)...)...)
	case opLEReadWhiteListSize:
		c.complete(opcode, statusSuccess, 16)
	case opVendorSetBDAddr:
		if len(p) >= 6 {
			c.hostAddr = reverse6(p[:6])
		}
		c.complete(opcode, statusSuccess)
	case opLESetRandomAddress:
		c.complete(opcode, statusSuccess)
	case opLESetAdvParams:
		c.complete(opcode, statusSuccess)
	case opLESetAdvData:
		if len(p) >= 1 {
			n := min(int(p[0]), len(p)-1)
			c.advData = append([]byte(nil), p[1:1+n]...)
		}
		c.complete(opcode, statusSuccess)
	case opLESetAdvEnable:
		c.advOn = len(p) > 0 && p[0] != 0
		c.complete(opcode, statusSuccess)
	case opLESetScanParams:
		c.complete(opcode, statusSuccess)
	case opLESetScanEnable:
		enable := len(p) > 0 && p[0] != 0
		c.setScan(enable)
		c.complete(opcode, statusSuccess)
	case opLECreateConnection:
		c.createConnection(p)
	case opLEConnectionUpdate:
		if len(p) >= 2 {
			handle := binary.LittleEndian.Uint16(p)
			c.connStatus(opcode, handle)
			if c.conn != nil && handle == c.conn.handle {
				c.updateDeadline.Schedule(c.chip.Cycles + 5*rp2350.CyclesPerMicrosecond)
			}
		} else {
			c.complete(opcode, statusSuccess)
		}
	case opLEReadRemoteFeatures:
		if len(p) >= 2 && c.conn != nil {
			c.connStatus(opcode, c.conn.handle)
			c.featuresDeadline.Schedule(c.chip.Cycles + 5*rp2350.CyclesPerMicrosecond)
		} else {
			c.complete(opcode, statusSuccess)
		}
	case opDisconnect:
		if len(p) >= 2 && c.conn != nil {
			c.connStatus(opcode, c.conn.handle)
			c.disconnectDeadline.Schedule(c.chip.Cycles + 2*rp2350.CyclesPerMicrosecond)
		} else {
			c.complete(opcode, statusSuccess)
		}
	default:
		// Never silently accept an unknown command: btstack will surface the
		// error and the opcode stays in the log.
		c.complete(opcode, statusUnknownCommand)
	}
}

func (c *Controller) resetForCommand() {
	c.conn = nil
	c.scanning = false
	c.advOn = false
	c.notifyLeft = 0
	c.peer = newPeer()
	c.events = nil
	for _, d := range []*rp2350.Deadline{
		c.scanDeadline, c.connectDeadline, c.disconnectDeadline,
		c.notifyDeadline, c.updateDeadline, c.featuresDeadline,
	} {
		if d != nil {
			d.Cancel()
		}
	}
	c.updateWake()
}

func (c *Controller) complete(opcode uint16, params ...byte) {
	c.queue(commandComplete(opcode, params))
}

func (c *Controller) connStatus(opcode uint16, handle uint16) {
	c.queue(commandStatus(opcode, statusSuccess))
}

func (c *Controller) setScan(enable bool) {
	c.scanning = enable
	c.scanDeadline.Cancel()
	if enable && c.conn == nil {
		c.sendAdvertisingReport()
		c.scanDeadline.Schedule(c.chip.Cycles + 100*rp2350.CyclesPerMicrosecond)
	}
}

func (c *Controller) scanTick() {
	if !c.scanning || c.conn != nil {
		return
	}
	c.sendAdvertisingReport()
	c.scanDeadline.Schedule(c.chip.Cycles + 100*rp2350.CyclesPerMicrosecond)
}

func (c *Controller) sendAdvertisingReport() {
	adv := c.advData
	if len(adv) == 0 {
		adv = defaultAdvData()
	}
	data := make([]byte, 0, 13+len(adv))
	data = append(data, 1)    // one report
	data = append(data, 0x00) // connectable undirected
	data = append(data, 0x00) // public address
	a := reverse6(c.addr[:])
	data = append(data, a[:]...)
	data = append(data, byte(len(adv)))
	data = append(data, adv...)
	data = append(data, byte(0xC0+c.rng.Intn(16))) // -64..-49 dBm
	c.queue(leMeta(leAdvertisingReport, data))
}

func (c *Controller) createConnection(p []byte) {
	if len(p) < 13 {
		c.complete(opLECreateConnection, statusUnknownCommand)
		return
	}
	peerType := p[5]
	peerAddr := reverse6(p[6:12])
	c.conn = &connection{
		handle:   connHandle,
		peerAddr: peerAddr,
		peerType: peerType,
		mtu:      defaultMTU,
	}
	c.scanning = false
	c.scanDeadline.Cancel()
	c.queue(commandStatus(opLECreateConnection, statusSuccess))
	c.connectDeadline.Schedule(c.chip.Cycles + 20*rp2350.CyclesPerMicrosecond)
}

func (c *Controller) connectComplete() {
	if c.conn == nil {
		return
	}
	data := make([]byte, 0, 19)
	data = append(data, statusSuccess)
	data = append(data, byte(c.conn.handle), byte(c.conn.handle>>8))
	data = append(data, 0x00) // role: central
	data = append(data, c.conn.peerType)
	a := reverse6(c.conn.peerAddr[:])
	data = append(data, a[:]...)
	data = append(data, byte(defaultInterval), byte(defaultInterval>>8))
	data = append(data, 0, 0)       // latency
	data = append(data, 0xC8, 0x00) // supervision timeout 2 s
	data = append(data, 0)          // master clock accuracy
	c.queue(leMeta(leConnectionComplete, data))
}

func (c *Controller) updateComplete() {
	if c.conn == nil {
		return
	}
	data := []byte{statusSuccess,
		byte(c.conn.handle), byte(c.conn.handle >> 8),
		byte(defaultInterval), byte(defaultInterval >> 8),
		0, 0,
		0xC8, 0x00}
	c.queue(leMeta(leConnectionUpdateComplete, data))
}

func (c *Controller) remoteFeaturesComplete() {
	if c.conn == nil {
		return
	}
	data := []byte{statusSuccess,
		byte(c.conn.handle), byte(c.conn.handle >> 8)}
	data = append(data, make([]byte, 8)...)
	c.queue(leMeta(leReadRemoteFeatures, data))
}

func (c *Controller) disconnectComplete() {
	if c.conn == nil {
		return
	}
	handle := c.conn.handle
	c.conn = nil
	c.notifyLeft = 0
	c.notifyDeadline.Cancel()
	c.queue(event(evtDisconnectionComplete, []byte{
		statusSuccess, byte(handle), byte(handle >> 8), 0x13, // remote user terminated
	}))
}

func (c *Controller) acl(data []byte) {
	if len(data) < 4 {
		return
	}
	handle := binary.LittleEndian.Uint16(data) & 0x0FFF
	length := int(binary.LittleEndian.Uint16(data[2:]))
	if 4+length > len(data) {
		length = len(data) - 4
	}
	payload := data[4 : 4+length]
	// Acknowledge the host packet so btstack's controller-to-host flow control
	// does not stall after MAX_NR_CONTROLLER_ACL_BUFFERS.
	c.queue(event(evtNumberOfCompletedPacket, []byte{
		1, byte(handle), byte(handle >> 8), 1, 0,
	}))
	if c.conn == nil || handle != c.conn.handle {
		return
	}
	pb := (binary.LittleEndian.Uint16(data) >> 12) & 0x3
	switch pb {
	case 0x2, 0x0: // first fragment
		c.conn.l2buf = append(c.conn.l2buf[:0], payload...)
	case 0x1: // continuation
		c.conn.l2buf = append(c.conn.l2buf, payload...)
	default:
		return
	}
	c.processL2CAP()
}

func (c *Controller) processL2CAP() {
	if c.conn == nil || len(c.conn.l2buf) < 4 {
		return
	}
	n := int(binary.LittleEndian.Uint16(c.conn.l2buf))
	if n < 0 || 4+n > len(c.conn.l2buf) {
		return
	}
	cid := binary.LittleEndian.Uint16(c.conn.l2buf[2:])
	payload := c.conn.l2buf[4 : 4+n]
	c.conn.l2buf = c.conn.l2buf[4+n:]
	switch cid {
	case attCID:
		resp, notify := c.peer.handleATT(payload)
		if resp != nil {
			c.sendL2CAP(attCID, resp)
		}
		if notify {
			c.notifyLeft = 8
			c.notifyDeadline.Schedule(c.chip.Cycles + 30*rp2350.CyclesPerMicrosecond)
		}
	}
}

func (c *Controller) sendL2CAP(cid uint16, payload []byte) {
	if c.conn == nil {
		return
	}
	c.queue(aclPacket(c.conn.handle, l2cap(cid, payload)))
}

func (c *Controller) notifyTick() {
	if c.conn == nil || c.notifyLeft <= 0 {
		return
	}
	if pkt := c.peer.notification(); pkt != nil {
		c.sendL2CAP(attCID, pkt)
	}
	c.notifyLeft--
	if c.notifyLeft > 0 {
		c.notifyDeadline.Schedule(c.chip.Cycles + 100*rp2350.CyclesPerMicrosecond)
	}
}

func reverse6(in []byte) [6]byte {
	var out [6]byte
	for i := 0; i < 6 && i < len(in); i++ {
		out[i] = in[5-i]
	}
	return out
}

func defaultAdvData() []byte {
	return []byte{
		0x02, 0x01, 0x06,
		0x03, 0x03, 0x1A, 0x18,
		0x0B, 0x09, 'r', 'p', '2', '3', '5', '0', 's', 'i', 'm',
	}
}

// supportedCommands returns the 64-byte HCI supported-commands bitmap. Only the
// bits btstack inspects are set; everything else is deliberately absent so
// btstack's optional init steps are skipped.
func supportedCommands() []byte {
	bm := make([]byte, 64)
	bm[14] |= 1 << 7 // Read Buffer Size
	bm[24] |= 1 << 6 // Write LE Host Supported
	return bm
}

// localFeatures returns the 8-byte LMP feature page 0. Byte 4 carries "BR/EDR
// not supported" (bit 5) and "LE supported (controller)" (bit 6).
func localFeatures() []byte {
	f := make([]byte, 8)
	f[4] = (1 << 5) | (1 << 6)
	return f
}

func (c *Controller) String() string {
	return fmt.Sprintf("ble controller addr=%02X:%02X:%02X:%02X:%02X:%02X",
		c.addr[0], c.addr[1], c.addr[2], c.addr[3], c.addr[4], c.addr[5])
}
