package devices

import (
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/akh/rp2350sim/internal/rp2350"
)

// W5500 models a WIZnet W5500 Ethernet controller as seen over SPI, with the
// eight hardware sockets backed by the host's network stack. TinyGo's stock
// w5500/netdev driver works unchanged: it configures the chip, then asks for
// TCP/UDP sockets, connect/listen/send/recv, and the model performs the
// corresponding Go net operations.
//
// The SPI framing is the W5500 one: three header bytes (address high, address
// low, control) followed by a data stream in the addressed block. The block
// select bits choose common registers, per-socket registers, or a socket's TX
// or RX buffer.
type W5500 struct {
	Chip *rp2350.Chip
	CS   int

	// Logf, when non-nil, receives transaction and socket diagnostics.
	Logf func(format string, args ...any)

	mu sync.Mutex

	common  [0x40]byte
	sockReg [8][0x30]byte
	txMem   [8][]byte
	rxMem   [8][]byte
	socks   [8]*w5sock

	// SPI transaction state.
	active  bool
	hcount  int
	header  [3]byte
	writing bool
	bsb     uint8
	addr    uint16

	closed bool
}

type w5sock struct {
	status   byte
	proto    byte // 1 TCP, 2 UDP
	srcPort  uint16
	destIP   [4]byte
	destPort uint16

	conn     net.Conn
	listener net.Listener
	udp      *net.UDPConn

	// Absolute buffer pointers, as the chip exposes them.
	txReadPtr  uint16
	txWritePtr uint16
	rxReadPtr  uint16
	rxWritePtr uint16
	rxPending  int

	ints      byte
	pendingTx []byte

	// gen invalidates callbacks from sockets that were closed and reopened.
	gen uint64
}

// W5500 register offsets and command/status values.
const (
	w5CommonMode     = 0x0000
	w5CommonPHYCfg   = 0x002E
	w5CommonChipVer  = 0x0039
	w5SockMode       = 0x0000
	w5SockCmd        = 0x0001
	w5SockInt        = 0x0002
	w5SockStatus     = 0x0003
	w5SockSrcPort    = 0x0004
	w5SockDestMAC    = 0x0006
	w5SockDestIP     = 0x000C
	w5SockDestPort   = 0x0010
	w5SockRXBufSize  = 0x001E
	w5SockTXBufSize  = 0x001F
	w5SockTXFreeSize = 0x0020
	w5SockTXReadPtr  = 0x0022
	w5SockTXWritePtr = 0x0024
	w5SockRXRecvSize = 0x0026
	w5SockRXReadPtr  = 0x0028
	w5SockRXWritePtr = 0x002A
)

const (
	w5CmdOpen       = 0x01
	w5CmdListen     = 0x02
	w5CmdConnect    = 0x04
	w5CmdDisconnect = 0x08
	w5CmdClose      = 0x10
	w5CmdSend       = 0x20
	w5CmdRecv       = 0x40
)

const (
	w5StatusClosed      = 0x00
	w5StatusInit        = 0x13
	w5StatusListen      = 0x14
	w5StatusEstablished = 0x17
	w5StatusCloseWait   = 0x1C
	w5StatusUDP         = 0x22
)

const (
	w5IntConnect    = 1 << 0
	w5IntDisconnect = 1 << 1
	w5IntReceive    = 1 << 2
	w5IntTimeout    = 1 << 3
	w5IntSendOK     = 1 << 4
)

// NewW5500 creates a W5500 attached to the chip's SPI bus with the given
// chip-select pin.
func NewW5500(chip *rp2350.Chip, cs int) *W5500 {
	w := &W5500{Chip: chip, CS: cs}
	for i := range w.socks {
		w.socks[i] = &w5sock{}
		w.txMem[i] = make([]byte, 16*1024)
		w.rxMem[i] = make([]byte, 16*1024)
	}
	return w
}

func (w *W5500) logf(format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
	}
}

// Tick samples chip select between SPI bytes, ending a transaction when the
// host raises CS. Register it with Chip.CycleHooks.
func (w *W5500) Tick() {
	if w.Chip.Pins.Level(w.CS) {
		w.active = false
		w.hcount = 0
	}
}

// Transfer feeds one SPI byte to the chip and returns the MISO byte.
func (w *W5500) Transfer(tx byte) byte {
	if w.Chip.Pins.Level(w.CS) {
		w.active = false
		w.hcount = 0
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.active {
		w.header[w.hcount] = tx
		w.hcount++
		if w.hcount < 3 {
			return 0
		}
		w.active = true
		w.bsb = w.header[2] >> 3
		w.addr = uint16(w.header[0])<<8 | uint16(w.header[1])
		w.writing = w.header[2]&0x04 != 0
		w.hcount = 0
		return 0
	}
	if w.writing {
		w.writeByte(tx)
		return 0
	}
	return w.readByte()
}

func (w *W5500) block(bsb uint8) (sock int, kind int) {
	switch bsb & 3 {
	case 0:
		return -1, 0 // common registers
	case 1:
		return int(bsb >> 2), 1 // socket registers
	case 2:
		return int(bsb >> 2), 2 // TX buffer
	default:
		return int(bsb >> 2), 3 // RX buffer
	}
}

func (w *W5500) readByte() byte {
	sock, kind := w.block(w.bsb)
	var v byte
	switch kind {
	case 0:
		off := w.addr & 0x3F
		switch off {
		case w5CommonChipVer:
			v = 0x04
		case w5CommonPHYCfg:
			// LINK is bit 0: the virtual PHY is always up.
			v = w.common[off] | 1
		default:
			v = w.common[off]
		}
	case 1:
		v = w.readSockReg(sock, w.addr&0x3F)
	case 2:
		v = w.txMem[sock][w.addr%uint16(w.sockTXSize(sock))]
	case 3:
		v = w.rxMem[sock][w.addr%uint16(w.sockRXSize(sock))]
	}
	w.addr++
	return v
}

func (w *W5500) writeByte(b byte) {
	sock, kind := w.block(w.bsb)
	switch kind {
	case 0:
		off := w.addr & 0x3F
		w.common[off] = b
		if off == w5CommonMode && b&0x80 != 0 {
			w.resetAll()
		}
	case 1:
		w.writeSockReg(sock, w.addr&0x3F, b)
	case 2:
		w.txMem[sock][w.addr%uint16(w.sockTXSize(sock))] = b
	case 3:
		// RX memory is written by the chip, not the host.
	}
	w.addr++
}

func (w *W5500) sockTXSize(n int) int {
	kb := int(w.sockReg[n][w5SockTXBufSize])
	if kb == 0 {
		kb = 2
	}
	if kb > 16 {
		kb = 16
	}
	return kb * 1024
}

func (w *W5500) sockRXSize(n int) int {
	kb := int(w.sockReg[n][w5SockRXBufSize])
	if kb == 0 {
		kb = 2
	}
	if kb > 16 {
		kb = 16
	}
	return kb * 1024
}

func (w *W5500) readSockReg(n int, off uint16) byte {
	s := w.socks[n]
	switch off {
	case w5SockStatus:
		return s.status
	case w5SockInt:
		return s.ints
	}
	// The 16-bit registers are big-endian; every pair maps through the
	// dynamic value.
	switch off {
	case w5SockTXFreeSize, w5SockTXFreeSize + 1,
		w5SockTXReadPtr, w5SockTXReadPtr + 1,
		w5SockTXWritePtr, w5SockTXWritePtr + 1,
		w5SockRXRecvSize, w5SockRXRecvSize + 1,
		w5SockRXReadPtr, w5SockRXReadPtr + 1,
		w5SockRXWritePtr, w5SockRXWritePtr + 1:
		base := off &^ 1
		v := w.readUint16Sock(n, base)
		if off&1 == 0 {
			return byte(v >> 8)
		}
		return byte(v)
	}
	return w.sockReg[n][off]
}

func (w *W5500) writeSockReg(n int, off uint16, b byte) {
	s := w.socks[n]
	w.sockReg[n][off] = b
	switch off {
	case w5SockInt:
		s.ints &^= b
		w.sockReg[n][off] = 0
	case w5SockCmd:
		w.sockReg[n][off] = 0 // commands clear once accepted
		w.command(n, b)
	case w5SockSrcPort:
		if off == w5SockSrcPort {
			s.srcPort = uint16(w.sockReg[n][w5SockSrcPort])<<8 | uint16(w.sockReg[n][w5SockSrcPort+1])
		}
	case w5SockSrcPort + 1:
		s.srcPort = uint16(w.sockReg[n][w5SockSrcPort])<<8 | uint16(w.sockReg[n][w5SockSrcPort+1])
	case w5SockDestIP:
		s.destIP[0] = b
	case w5SockDestIP + 1:
		s.destIP[1] = b
	case w5SockDestIP + 2:
		s.destIP[2] = b
	case w5SockDestIP + 3:
		s.destIP[3] = b
	case w5SockDestPort:
		s.destPort = uint16(b)<<8 | uint16(w.sockReg[n][w5SockDestPort+1])
	case w5SockDestPort + 1:
		s.destPort = uint16(w.sockReg[n][w5SockDestPort])<<8 | uint16(b)
		w.logf("w5500: sock%d dest %d.%d.%d.%d:%d", n, s.destIP[0], s.destIP[1], s.destIP[2], s.destIP[3], s.destPort)
	case w5SockTXWritePtr:
		s.txWritePtr = uint16(b)<<8 | uint16(w.sockReg[n][w5SockTXWritePtr+1])
	case w5SockTXWritePtr + 1:
		s.txWritePtr = uint16(w.sockReg[n][w5SockTXWritePtr])<<8 | uint16(b)
	case w5SockRXReadPtr:
		s.rxReadPtr = uint16(b)<<8 | uint16(w.sockReg[n][w5SockRXReadPtr+1])
	case w5SockRXReadPtr + 1:
		s.rxReadPtr = uint16(w.sockReg[n][w5SockRXReadPtr])<<8 | uint16(b)
	}
}

// readUint16Sock reads a 16-bit socket register.
func (w *W5500) readUint16Sock(n int, off uint16) uint16 {
	s := w.socks[n]
	switch off {
	case w5SockTXFreeSize:
		used := s.txWritePtr - s.txReadPtr
		free := uint16(w.sockTXSize(n)) - used
		return free
	case w5SockTXReadPtr:
		return s.txReadPtr
	case w5SockTXWritePtr:
		return s.txWritePtr
	case w5SockRXRecvSize:
		return uint16(s.rxPending)
	case w5SockRXReadPtr:
		return s.rxReadPtr
	case w5SockRXWritePtr:
		return s.rxWritePtr
	}
	return uint16(w.sockReg[n][off])<<8 | uint16(w.sockReg[n][off+1])
}

func (w *W5500) resetAll() {
	w.common = [0x40]byte{}
	for i := range w.socks {
		w.closeSocket(i)
		w.sockReg[i] = [0x30]byte{}
	}
	w.logf("w5500: reset")
}

func (w *W5500) command(n int, cmd byte) {
	s := w.socks[n]
	switch cmd {
	case w5CmdOpen:
		if s.status != w5StatusClosed && s.status != w5StatusInit {
			w.closeSocket(n)
		}
		s.proto = w.sockReg[n][w5SockMode] & 0x0F
		switch s.proto {
		case 1:
			s.status = w5StatusInit
			// TinyGo's driver issues OPEN (not CONNECT) after setting the
			// destination; treat that as the connection request.
			if s.destPort != 0 {
				w.connect(n)
			}
		case 2:
			s.status = w5StatusUDP
		default:
			s.status = w5StatusClosed
		}
		w.logf("w5500: sock%d open proto=%d port=%d", n, s.proto, s.srcPort)
	case w5CmdClose:
		w.closeSocket(n)
	case w5CmdConnect:
		w.connect(n)
	case w5CmdListen:
		w.listen(n)
	case w5CmdDisconnect:
		w.closeSocket(n)
		s.ints |= w5IntDisconnect
	case w5CmdSend:
		w.send(n)
	case w5CmdRecv:
		s.rxPending = 0
		s.rxReadPtr = s.rxWritePtr
		s.ints &^= w5IntReceive
	}
}

func (w *W5500) connect(n int) {
	s := w.socks[n]
	gen := s.gen
	dst := net.JoinHostPort(net.IP(s.destIP[:]).String(), strconv.Itoa(int(s.destPort)))
	w.logf("w5500: sock%d connect %s", n, dst)
	if s.proto == 2 {
		// UDP: bind a local socket and connect it to the destination.
		conn, err := net.Dial("udp", dst)
		if err != nil {
			s.status = w5StatusClosed
			s.ints |= w5IntTimeout
			return
		}
		if uc, ok := conn.(*net.UDPConn); ok {
			s.udp = uc
			go w.udpReader(n, gen, uc)
		}
		s.conn = conn
		s.status = w5StatusUDP
		s.ints |= w5IntConnect
		return
	}
	s.status = w5StatusInit
	go func() {
		conn, err := net.DialTimeout("tcp", dst, 10*time.Second)
		w.mu.Lock()
		defer w.mu.Unlock()
		if s.gen != gen {
			if conn != nil {
				conn.Close()
			}
			return
		}
		if err != nil {
			s.status = w5StatusClosed
			s.ints |= w5IntTimeout
			w.logf("w5500: sock%d connect failed: %v", n, err)
			return
		}
		s.conn = conn
		s.status = w5StatusEstablished
		s.ints |= w5IntConnect
		if len(s.pendingTx) > 0 {
			// A SEND issued while the connection was in progress, as on the
			// hardware where the command is deferred until ESTABLISHED.
			if _, err := conn.Write(s.pendingTx); err != nil {
				s.ints |= w5IntTimeout
			} else {
				s.ints |= w5IntSendOK
			}
			s.pendingTx = nil
		}
		go w.tcpReader(n, gen, conn)
		w.logf("w5500: sock%d established", n)
	}()
}

func (w *W5500) listen(n int) {
	s := w.socks[n]
	gen := s.gen
	addr := ":"
	if s.srcPort != 0 {
		addr = ":" + strconv.Itoa(int(s.srcPort))
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.status = w5StatusClosed
		s.ints |= w5IntTimeout
		w.logf("w5500: sock%d listen %s failed: %v", n, addr, err)
		return
	}
	s.listener = ln
	s.status = w5StatusListen
	w.logf("w5500: sock%d listen %s", n, addr)
	go func() {
		conn, err := ln.Accept()
		w.mu.Lock()
		defer w.mu.Unlock()
		if s.gen != gen {
			if conn != nil {
				conn.Close()
			}
			return
		}
		if err != nil {
			return
		}
		s.conn = conn
		s.status = w5StatusEstablished
		s.ints |= w5IntConnect
		if ta, ok := conn.RemoteAddr().(*net.TCPAddr); ok && ta.IP.To4() != nil {
			copy(s.destIP[:], ta.IP.To4())
			s.destPort = uint16(ta.Port)
			w.sockReg[n][w5SockDestIP] = s.destIP[0]
			w.sockReg[n][w5SockDestIP+1] = s.destIP[1]
			w.sockReg[n][w5SockDestIP+2] = s.destIP[2]
			w.sockReg[n][w5SockDestIP+3] = s.destIP[3]
			w.sockReg[n][w5SockDestPort] = byte(s.destPort >> 8)
			w.sockReg[n][w5SockDestPort+1] = byte(s.destPort)
		}
		go w.tcpReader(n, gen, conn)
		w.logf("w5500: sock%d accepted %s", n, conn.RemoteAddr())
	}()
}

func (w *W5500) send(n int) {
	s := w.socks[n]
	size := uint16(w.sockTXSize(n))
	count := s.txWritePtr - s.txReadPtr
	if count > size {
		count = size
	}
	data := make([]byte, 0, count)
	for i := uint16(0); i < count; i++ {
		data = append(data, w.txMem[n][(s.txReadPtr+i)%size])
	}
	s.txReadPtr = s.txWritePtr

	if s.conn == nil && s.proto == 1 {
		// Connection still in progress: defer the payload.
		s.pendingTx = append(s.pendingTx, data...)
		return
	}
	var err error
	if s.udp == nil && s.conn == nil && s.proto == 2 {
		dst := net.JoinHostPort(net.IP(s.destIP[:]).String(), strconv.Itoa(int(s.destPort)))
		local := ""
		if s.srcPort != 0 {
			local = ":" + strconv.Itoa(int(s.srcPort))
		}
		var conn *net.UDPConn
		w.logf("w5500: sock%d lazy udp dial %s", n, dst)
		conn, err = net.DialUDP("udp", nil, resolveUDP(dst))
		if err == nil {
			s.udp = conn
			go w.udpReader(n, s.gen, conn)
		}
		_ = local
	}
	if s.udp != nil {
		_, err = s.udp.Write(data)
	} else if s.conn != nil {
		_, err = s.conn.Write(data)
	} else {
		err = fmt.Errorf("socket not connected")
	}
	if err != nil {
		s.ints |= w5IntTimeout
		w.logf("w5500: sock%d send %d bytes failed: %v", n, len(data), err)
		return
	}
	s.ints |= w5IntSendOK
	w.logf("w5500: sock%d send %d bytes", n, len(data))
}

func (w *W5500) tcpReader(n int, gen uint64, conn net.Conn) {
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
			if s.status == w5StatusEstablished {
				s.status = w5StatusCloseWait
			}
			s.ints |= w5IntDisconnect
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()
	}
}

func (w *W5500) udpReader(n int, gen uint64, conn *net.UDPConn) {
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
		w.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func resolveUDP(addr string) *net.UDPAddr {
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil
	}
	return a
}

func (w *W5500) rxAppend(n int, data []byte) {
	s := w.socks[n]
	size := w.sockRXSize(n)
	for _, b := range data {
		if s.rxPending >= size {
			break // hardware drops data when the RX buffer is full
		}
		w.rxMem[n][s.rxWritePtr%uint16(size)] = b
		s.rxWritePtr++
		s.rxPending++
	}
	s.ints |= w5IntReceive
}

func (w *W5500) closeSocket(n int) {
	s := w.socks[n]
	s.gen++
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
	if s.listener != nil {
		s.listener.Close()
		s.listener = nil
	}
	s.udp = nil
	s.status = w5StatusClosed
	s.ints = 0
	s.txReadPtr, s.txWritePtr = 0, 0
	s.rxReadPtr, s.rxWritePtr = 0, 0
	s.rxPending = 0
}
