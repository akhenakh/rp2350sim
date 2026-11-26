package devices

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// W6300 models the WIZnet W6300 Ethernet controller, whose sockets are backed
// by the host's network stack. The host interface is the W6300 single-SPI
// frame: an instruction byte (mode/RWB/block select), a 16-bit address, one
// dummy byte, then the data stream.
type W6300 struct {
	Chip *rp2350.Chip
	CS   int

	// Logger, when non-nil, reports socket events.
	Logger *slog.Logger
	// WriteTimeout bounds synchronous socket writes. Zero uses 2 seconds.
	WriteTimeout time.Duration

	mu        sync.Mutex
	closed    bool
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once

	common  [0x4210]byte
	sockReg [8][0x400]byte
	txMem   [8][]byte
	rxMem   [8][]byte
	socks   [8]*w6sock

	// SPI frame state.
	active bool
	phase  int // 0 instruction, 1 addr hi, 2 addr lo, 3 dummy, 4 data
	instr  byte
	bsb    uint8
	write  bool
	addr   uint16
}

type w6sock struct {
	status   byte
	proto    byte
	srcPort  uint16
	destIP   [4]byte
	destPort uint16

	conn     net.Conn
	listener net.Listener
	udp      *net.UDPConn
	// udpServer is set when the socket owns a bound listener (UDP server)
	// rather than a connected client.
	udpServer bool

	txReadPtr  uint16
	txWritePtr uint16
	rxReadPtr  uint16
	rxReadAck  uint16
	rxWritePtr uint16
	rxPending  int

	irq       byte
	pendingTx []byte
	gen       uint64
}

// Block select values.
const (
	w6BlockCommon = 0
	w6BlockRegs   = 1
	w6BlockTX     = 2
	w6BlockRX     = 3
)

// Common register offsets.
const (
	w6CIDR0  = 0x0000
	w6CIDR1  = 0x0001
	w6CIDR2  = 0x0004
	w6SYSR   = 0x2000
	w6SYCR0  = 0x2004
	w6SYCR1  = 0x2005
	w6IR     = 0x2100
	w6SIR    = 0x2101
	w6IMR    = 0x2104
	w6IRCLR  = 0x2108
	w6SIMR   = 0x2114
	w6PHYSR  = 0x3000
	w6PHYRAR = 0x3008
	w6PHYDOR = 0x3010
	w6NET4MR = 0x4000
	w6NETMR  = 0x4008
	w6SHAR   = 0x4120
	w6GAR    = 0x4130
	w6SUBR   = 0x4134
	w6SIPR   = 0x4138
)

// Socket register offsets.
const (
	w6SnMR     = 0x0000
	w6SnCR     = 0x0010
	w6SnIR     = 0x0020
	w6SnIMR    = 0x0024
	w6SnIRCLR  = 0x0028
	w6SnSR     = 0x0030
	w6SnPORTR  = 0x0114
	w6SnDHAR   = 0x0118
	w6SnDIPR   = 0x0120
	w6SnDPORTR = 0x0140
	w6SnTXBSR  = 0x0200
	w6SnTXFSR  = 0x0204
	w6SnTXRD   = 0x0208
	w6SnTXWR   = 0x020C
	w6SnRXBSR  = 0x0220
	w6SnRXRSR  = 0x0224
	w6SnRXRD   = 0x0228
	w6SnRXWR   = 0x022C
)

// Socket commands.
const (
	w6CmdOpen    = 0x01
	w6CmdListen  = 0x02
	w6CmdConnect = 0x04
	w6CmdDiscon  = 0x08
	w6CmdClose   = 0x10
	w6CmdSend    = 0x20
	w6CmdRecv    = 0x40
)

// Socket statuses.
const (
	w6StatusClosed      = 0x00
	w6StatusInit        = 0x13
	w6StatusListen      = 0x14
	w6StatusEstablished = 0x17
	w6StatusCloseWait   = 0x1C
	w6StatusUDP         = 0x22
)

// Socket interrupt bits.
const (
	w6IrCon     = 1 << 0
	w6IrDiscon  = 1 << 1
	w6IrRecv    = 1 << 2
	w6IrTimeout = 1 << 3
	w6IrSendOK  = 1 << 4
)

// NewW6300 creates a W6300 attached to the chip's SPI bus.
func NewW6300(chip *rp2350.Chip, cs int) *W6300 {
	ctx, cancel := context.WithCancel(context.Background())
	w := &W6300{Chip: chip, CS: cs, ctx: ctx, cancel: cancel}
	for i := range w.socks {
		w.socks[i] = &w6sock{}
		w.txMem[i] = make([]byte, 32*1024)
		w.rxMem[i] = make([]byte, 32*1024)
		w.sockReg[i][w6SnTXBSR] = 4
		w.sockReg[i][w6SnRXBSR] = 4
	}
	// Chip identification: W6300, major 0x6100, minor 0x11.
	w.common[w6CIDR0] = 0x61
	w.common[w6CIDR1] = 0x00
	w.common[w6CIDR2] = 0x11
	w.common[w6SYCR0] = 0x80
	w.common[w6SYCR1] = 0x80
	chip.Pins.AddChangeListener(w.csEdge)
	return w
}

// csEdge ends the current frame when the host raises chip select.
func (w *W6300) csEdge(pin int, level bool) {
	if pin != w.CS || !level {
		return
	}
	w.mu.Lock()
	w.active = false
	w.phase = 0
	w.mu.Unlock()
}

// Close cancels pending dials, closes every socket and joins network goroutines.
func (w *W6300) Close() {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		if w.cancel != nil {
			w.cancel()
		}
		for i := range w.socks {
			w.closeSocket(i)
		}
		w.active = false
		w.phase = 0
		w.mu.Unlock()
		w.wg.Wait()
	})
}

func (w *W6300) startNetworkLocked(fn func()) bool {
	if w.closed {
		return false
	}
	w.wg.Go(fn)
	return true
}

func (w *W6300) dialContext(network, addr string) (net.Conn, error) {
	ctx := w.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
}

// Transfer feeds one SPI byte to the chip and returns the MISO byte. A raised
// chip select ends the current frame; checking it here is exactly when the
// next byte would be sampled, so no per-cycle poll is needed.
func (w *W6300) Transfer(tx byte) byte {
	if w.Chip.Pins.Level(w.CS) {
		w.active = false
		w.phase = 0
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.active {
		w.active = true
		w.phase = 0
	}
	switch w.phase {
	case 0: // instruction
		w.instr = tx
		w.write = tx&0x20 != 0
		w.bsb = tx & 0x1F
		w.phase = 1
		return 0
	case 1: // address high
		w.addr = uint16(tx) << 8
		w.phase = 2
		return 0
	case 2: // address low
		w.addr |= uint16(tx)
		w.phase = 3
		return 0
	case 3: // dummy
		w.phase = 4
		return 0
	}
	if w.write {
		w.writeByte(tx)
		return 0
	}
	return w.readByte()
}

func (w *W6300) block(bsb uint8) (sock int, kind int) {
	kind = int(bsb & 3)
	if kind == 0 {
		return -1, w6BlockCommon
	}
	return int(bsb >> 2), kind
}

func (w *W6300) readByte() byte {
	sock, kind := w.block(w.bsb)
	var v byte
	switch kind {
	case w6BlockCommon:
		v = w.readCommon(w.addr)
	case w6BlockRegs:
		v = w.readSockReg(sock, w.addr&0x3FF)
	case w6BlockTX:
		v = w.txMem[sock][w.addr%uint16(w.sockTXSize(sock))]
	case w6BlockRX:
		v = w.rxMem[sock][w.addr%uint16(w.sockRXSize(sock))]
	}
	w.addr++
	return v
}

func (w *W6300) writeByte(b byte) {
	sock, kind := w.block(w.bsb)
	switch kind {
	case w6BlockCommon:
		w.writeCommon(w.addr, b)
	case w6BlockRegs:
		w.writeSockReg(sock, w.addr&0x3FF, b)
	case w6BlockTX:
		w.txMem[sock][w.addr%uint16(w.sockTXSize(sock))] = b
	}
	w.addr++
}

func (w *W6300) readCommon(off uint16) byte {
	switch off {
	case w6SYSR:
		return 1 // SPI host interface
	case w6PHYSR:
		return 1 // link up, 100 Mbps full duplex
	case w6IRCLR, w6SIMR:
		return 0
	}
	return w.common[off%uint16(len(w.common))]
}

func (w *W6300) writeCommon(off uint16, b byte) {
	if int(off) >= len(w.common) {
		return
	}
	switch off {
	case w6IRCLR:
		w.common[w6IR] &^= b
		return
	case w6SYCR0:
		// RST is active low: 0 resets the chip, 1 returns to normal
		// operation.
		w.common[w6SYCR0] = b
		if b&0x80 == 0 {
			w.softwareReset()
		}
		return
	}
	w.common[off] = b
}

func (w *W6300) sockTXSize(n int) int {
	kb := int(w.sockReg[n][w6SnTXBSR])
	if kb == 0 {
		kb = 4
	}
	if kb > 32 {
		kb = 32
	}
	return kb * 1024
}

func (w *W6300) sockRXSize(n int) int {
	kb := int(w.sockReg[n][w6SnRXBSR])
	if kb == 0 {
		kb = 4
	}
	if kb > 32 {
		kb = 32
	}
	return kb * 1024
}

func (w *W6300) readSockReg(n int, off uint16) byte {
	s := w.socks[n]
	switch off {
	case w6SnSR:
		return s.status
	case w6SnIR:
		return s.irq
	}
	switch off {
	case w6SnTXFSR, w6SnTXFSR + 1,
		w6SnTXRD, w6SnTXRD + 1,
		w6SnTXWR, w6SnTXWR + 1,
		w6SnRXRSR, w6SnRXRSR + 1,
		w6SnRXRD, w6SnRXRD + 1,
		w6SnRXWR, w6SnRXWR + 1:
		base := off &^ 1
		v := w.readUint16Sock(n, base)
		if off&1 == 0 {
			return byte(v >> 8)
		}
		return byte(v)
	}
	return w.sockReg[n][off]
}

func (w *W6300) writeSockReg(n int, off uint16, b byte) {
	s := w.socks[n]
	switch off {
	case w6SnIRCLR:
		s.irq &^= b
		return
	case w6SnCR:
		w.sockReg[n][off] = 0
		w.command(n, b)
		return
	}
	w.sockReg[n][off] = b
	switch off {
	case w6SnPORTR:
		s.srcPort = uint16(b)<<8 | uint16(w.sockReg[n][w6SnPORTR+1])
	case w6SnPORTR + 1:
		s.srcPort = uint16(w.sockReg[n][w6SnPORTR])<<8 | uint16(b)
	case w6SnDIPR:
		s.destIP[0] = b
	case w6SnDIPR + 1:
		s.destIP[1] = b
	case w6SnDIPR + 2:
		s.destIP[2] = b
	case w6SnDIPR + 3:
		s.destIP[3] = b
	case w6SnDPORTR:
		s.destPort = uint16(b)<<8 | uint16(w.sockReg[n][w6SnDPORTR+1])
	case w6SnDPORTR + 1:
		s.destPort = uint16(w.sockReg[n][w6SnDPORTR])<<8 | uint16(b)
	case w6SnTXWR:
		s.txWritePtr = uint16(b)<<8 | uint16(w.sockReg[n][w6SnTXWR+1])
	case w6SnTXWR + 1:
		s.txWritePtr = uint16(w.sockReg[n][w6SnTXWR])<<8 | uint16(b)
	case w6SnRXRD:
		s.rxReadPtr = uint16(b)<<8 | uint16(w.sockReg[n][w6SnRXRD+1])
	case w6SnRXRD + 1:
		s.rxReadPtr = uint16(w.sockReg[n][w6SnRXRD])<<8 | uint16(b)
	}
}

func (w *W6300) readUint16Sock(n int, off uint16) uint16 {
	s := w.socks[n]
	switch off {
	case w6SnTXFSR:
		used := s.txWritePtr - s.txReadPtr
		return uint16(w.sockTXSize(n)) - used
	case w6SnTXRD:
		return s.txReadPtr
	case w6SnTXWR:
		return s.txWritePtr
	case w6SnRXRSR:
		return uint16(s.rxPending)
	case w6SnRXRD:
		return s.rxReadPtr
	case w6SnRXWR:
		return s.rxWritePtr
	}
	return uint16(w.sockReg[n][off])<<8 | uint16(w.sockReg[n][off+1])
}

func (w *W6300) command(n int, cmd byte) {
	s := w.socks[n]
	switch cmd {
	case w6CmdOpen:
		if s.status != w6StatusClosed && s.status != w6StatusInit {
			w.closeSocket(n)
		}
		s.proto = w.sockReg[n][w6SnMR] & 0x0F
		switch s.proto {
		case 1:
			s.status = w6StatusInit
		case 2:
			s.status = w6StatusUDP
		default:
			s.status = w6StatusClosed
		}
		loggerOr(w.Logger).Debug("w6300 open", slog.Int("sock", n), slog.Int("proto", int(s.proto)), slog.Int("port", int(s.srcPort)))
	case w6CmdClose:
		w.closeSocket(n)
	case w6CmdDiscon:
		w.closeSocket(n)
		s.irq |= w6IrDiscon
	case w6CmdConnect:
		w.connect(n)
	case w6CmdListen:
		if s.proto == 2 {
			w.bindUDP(n)
		} else {
			w.listen(n)
		}
	case w6CmdSend:
		w.send(n)
	case w6CmdRecv:
		if s.proto == 2 && s.udp == nil {
			w.bindUDP(n)
		}
		// Only the bytes the host actually consumed (by advancing
		// Sn_RX_RD) leave the RX buffer.
		consumed := min(int(s.rxReadPtr-s.rxReadAck), s.rxPending)
		s.rxPending -= consumed
		s.rxReadAck = s.rxReadPtr
		if s.rxPending == 0 {
			s.irq &^= w6IrRecv
		}
	}
}

func (w *W6300) connect(n int) {
	if w.closed {
		return
	}
	s := w.socks[n]
	gen := s.gen
	dst := net.JoinHostPort(net.IP(s.destIP[:]).String(), strconv.Itoa(int(s.destPort)))
	loggerOr(w.Logger).Debug("w6300 connect", slog.Int("sock", n), slog.String("target", dst))
	if s.proto == 2 {
		var laddr *net.UDPAddr
		if s.srcPort != 0 {
			laddr = &net.UDPAddr{Port: int(s.srcPort)}
		}
		if s.udpServer && s.udp != nil {
			_ = s.udp.Close()
			s.udpServer = false
		}
		uc, err := net.DialUDP("udp", laddr, resolveUDPAddr(dst))
		if err != nil {
			s.status = w6StatusClosed
			s.irq |= w6IrTimeout
			return
		}
		s.udp = uc
		s.conn = uc
		s.udpServer = false
		s.status = w6StatusUDP
		s.irq |= w6IrCon
		w.startNetworkLocked(func() { w.udpReader(n, gen, uc) })
		return
	}
	s.status = w6StatusInit
	w.startNetworkLocked(func() {
		conn, err := w.dialContext("tcp", dst)
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.closed || s.gen != gen {
			if conn != nil {
				_ = conn.Close()
			}
			return
		}
		if err != nil {
			s.status = w6StatusClosed
			s.irq |= w6IrTimeout
			loggerOr(w.Logger).Debug("w6300 connect failed", slog.Int("sock", n), slog.Any("err", err))
			return
		}
		s.conn = conn
		s.status = w6StatusEstablished
		s.irq |= w6IrCon
		if len(s.pendingTx) > 0 {
			if _, err := writeNet(conn, s.pendingTx, w.WriteTimeout); err != nil {
				w.closeSocket(n)
				s.irq |= w6IrTimeout
				loggerOr(w.Logger).Debug("w6300 pending send failed", slog.Int("sock", n), slog.Any("err", err))
				return
			}
			s.irq |= w6IrSendOK
			s.pendingTx = nil
		}
		w.startNetworkLocked(func() { w.tcpReader(n, gen, conn) })
		loggerOr(w.Logger).Debug("w6300 established", slog.Int("sock", n))
	})
}

// bindUDP binds the guest source port and starts the datagram reader. A
// W6300 UDP socket with no CONNECT is a server.
func (w *W6300) bindUDP(n int) {
	if w.closed {
		return
	}
	s := w.socks[n]
	gen := s.gen
	addr := &net.UDPAddr{Port: int(s.srcPort)}
	uc, err := net.ListenUDP("udp", addr)
	if err != nil {
		s.status = w6StatusClosed
		s.irq |= w6IrTimeout
		loggerOr(w.Logger).Debug("w6300 udp bind failed", slog.Int("sock", n), slog.String("addr", addr.String()), slog.Any("err", err))
		return
	}
	s.udp = uc
	s.udpServer = true
	s.status = w6StatusUDP
	loggerOr(w.Logger).Debug("w6300 udp bind", slog.Int("sock", n), slog.String("addr", addr.String()))
	w.startNetworkLocked(func() { w.udpReader(n, gen, uc) })
}

func (w *W6300) listen(n int) {
	if w.closed {
		return
	}
	s := w.socks[n]
	gen := s.gen
	addr := ":"
	if s.srcPort != 0 {
		addr = ":" + strconv.Itoa(int(s.srcPort))
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.status = w6StatusClosed
		s.irq |= w6IrTimeout
		loggerOr(w.Logger).Debug("w6300 listen failed", slog.Int("sock", n), slog.String("addr", addr), slog.Any("err", err))
		return
	}
	s.listener = ln
	s.status = w6StatusListen
	loggerOr(w.Logger).Debug("w6300 listen", slog.Int("sock", n), slog.String("addr", addr))
	w.startNetworkLocked(func() {
		conn, err := ln.Accept()
		// The host listener owns the port while waiting; the accepted
		// connection no longer needs it, and the driver typically
		// re-listens on another hardware socket right away.
		_ = ln.Close()
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.closed || s.gen != gen {
			if conn != nil {
				_ = conn.Close()
			}
			return
		}
		if err != nil {
			return
		}
		s.listener = nil
		s.conn = conn
		s.status = w6StatusEstablished
		s.irq |= w6IrCon
		if ta, ok := conn.RemoteAddr().(*net.TCPAddr); ok && ta.IP.To4() != nil {
			copy(s.destIP[:], ta.IP.To4())
			s.destPort = uint16(ta.Port)
			w.sockReg[n][w6SnDIPR] = s.destIP[0]
			w.sockReg[n][w6SnDIPR+1] = s.destIP[1]
			w.sockReg[n][w6SnDIPR+2] = s.destIP[2]
			w.sockReg[n][w6SnDIPR+3] = s.destIP[3]
			w.sockReg[n][w6SnDPORTR] = byte(s.destPort >> 8)
			w.sockReg[n][w6SnDPORTR+1] = byte(s.destPort)
		}
		w.startNetworkLocked(func() { w.tcpReader(n, gen, conn) })
		loggerOr(w.Logger).Debug("w6300 accepted", slog.Int("sock", n), slog.String("remote", conn.RemoteAddr().String()))
	})
}

func (w *W6300) send(n int) {
	if w.closed {
		return
	}
	s := w.socks[n]
	size := uint16(w.sockTXSize(n))
	count := min(s.txWritePtr-s.txReadPtr, size)
	data := make([]byte, 0, count)
	for i := range count {
		data = append(data, w.txMem[n][(s.txReadPtr+i)%size])
	}
	s.txReadPtr = s.txWritePtr

	if s.conn == nil && s.proto == 1 {
		s.pendingTx = append(s.pendingTx, data...)
		return
	}
	var err error
	if s.udp == nil && s.conn == nil && s.proto == 2 {
		// UDP without CONNECT: dial the destination lazily.
		dst := net.JoinHostPort(net.IP(s.destIP[:]).String(), strconv.Itoa(int(s.destPort)))
		var laddr *net.UDPAddr
		if s.srcPort != 0 {
			laddr = &net.UDPAddr{Port: int(s.srcPort)}
		}
		var conn *net.UDPConn
		conn, err = net.DialUDP("udp", laddr, resolveUDPAddr(dst))
		if err == nil {
			s.udp = conn
			s.udpServer = false
			gen := s.gen
			w.startNetworkLocked(func() { w.udpReader(n, gen, conn) })
		}
	}
	if s.udp != nil && s.udpServer {
		_, err = writeUDPNet(s.udp, data, resolveUDPAddr(net.JoinHostPort(net.IP(s.destIP[:]).String(), strconv.Itoa(int(s.destPort)))), w.WriteTimeout)
	} else if s.udp != nil {
		_, err = writeNet(s.udp, data, w.WriteTimeout)
	} else if s.conn != nil {
		_, err = writeNet(s.conn, data, w.WriteTimeout)
	} else {
		err = fmt.Errorf("socket not connected")
	}
	if err != nil {
		w.closeSocket(n)
		s.irq |= w6IrTimeout
		loggerOr(w.Logger).Debug("w6300 send failed", slog.Int("sock", n), slog.Int("bytes", len(data)), slog.Any("err", err))
		return
	}
	s.irq |= w6IrSendOK
	loggerOr(w.Logger).Debug("w6300 send", slog.Int("sock", n), slog.Int("bytes", len(data)))
}

func resolveUDPAddr(addr string) *net.UDPAddr {
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil
	}
	return a
}

func (w *W6300) tcpReader(n int, gen uint64, conn net.Conn) {
	buf := make([]byte, 2048)
	for {
		m, err := conn.Read(buf)
		w.mu.Lock()
		if s := w.socks[n]; s.gen != gen {
			w.mu.Unlock()
			return
		}
		if m > 0 {
			w.rxAppend(n, buf[:m])
		}
		if err != nil {
			s := w.socks[n]
			if s.status == w6StatusEstablished {
				s.status = w6StatusCloseWait
			}
			s.irq |= w6IrDiscon
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()
	}
}

func (w *W6300) udpReader(n int, gen uint64, conn *net.UDPConn) {
	buf := make([]byte, 2048)
	for {
		m, peer, err := conn.ReadFromUDP(buf)
		w.mu.Lock()
		if s := w.socks[n]; s.gen != gen {
			w.mu.Unlock()
			return
		}
		if m > 0 {
			w.rxAppendUDP(n, buf[:m], peer)
		}
		w.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// rxAppendUDP stores the WIZnet 8-byte datagram header (peer IPv4, peer port,
// payload length) followed by the payload.
func (w *W6300) rxAppendUDP(n int, data []byte, peer *net.UDPAddr) {
	var hdr [8]byte
	if peer != nil {
		if ip := peer.IP.To4(); ip != nil {
			copy(hdr[:4], ip)
		}
		hdr[4] = byte(peer.Port >> 8)
		hdr[5] = byte(peer.Port)
	}
	hdr[6] = byte(len(data) >> 8)
	hdr[7] = byte(len(data))
	w.rxAppend(n, hdr[:])
	w.rxAppend(n, data)
}

func (w *W6300) rxAppend(n int, data []byte) {
	s := w.socks[n]
	size := w.sockRXSize(n)
	for _, b := range data {
		if s.rxPending >= size {
			break
		}
		w.rxMem[n][s.rxWritePtr%uint16(size)] = b
		s.rxWritePtr++
		s.rxPending++
	}
	s.irq |= w6IrRecv
}

func (w *W6300) closeSocket(n int) {
	if n < 0 || n >= len(w.socks) || w.socks[n] == nil {
		return
	}
	s := w.socks[n]
	s.gen++
	if s.udp != nil {
		// The CONNECT path sets both conn and udp to the same socket; the lazy
		// send path only sets udp. Close it here so the fd and udpReader
		// goroutine do not leak.
		if s.conn == nil {
			_ = s.udp.Close()
		}
		s.udp = nil
	}
	s.udpServer = false
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
	if s.listener != nil {
		_ = s.listener.Close()
		s.listener = nil
	}
	s.pendingTx = nil
	s.status = w6StatusClosed
	s.irq = 0
	s.pendingTx = nil
	s.txReadPtr, s.txWritePtr = 0, 0
	s.rxReadPtr, s.rxReadAck, s.rxWritePtr = 0, 0, 0
	s.rxPending = 0
}

// softwareReset restores the register defaults after SYCR0[RST] is driven low.
func (w *W6300) softwareReset() {
	for i := range w.socks {
		w.closeSocket(i)
		w.sockReg[i] = [0x400]byte{}
		w.sockReg[i][w6SnTXBSR] = 4
		w.sockReg[i][w6SnRXBSR] = 4
	}
	w.common[w6SYCR0] = 0
	w.common[w6SYCR1] = 0x80
}
