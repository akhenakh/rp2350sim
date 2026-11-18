package devices

import (
	"fmt"
	"net"
	"strconv"
	"sync"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// CH9120 models the WCH CH9120 UART-to-Ethernet transparent transmission
// chip fitted to the Waveshare RP2350-ETH (non-PoE). It speaks the serial
// configuration command set when CFG0 is low and forwards bytes to a socket
// when CFG0 is high. Sockets are backed by the host's network stack.
type CH9120 struct {
	Chip *rp2350.Chip
	// UART selects which hardware UART the module is wired to.
	UART int
	// CFG0, RST and TCPCS are the control pins (set to -1 to omit).
	CFG0, RST, TCPCS int

	// Logf, when non-nil, reports configuration and socket events.
	Logf func(format string, args ...any)
	// ReplyTap, when non-nil, also receives every byte sent to the MCU.
	ReplyTap func([]byte)
	// OnByte, when non-nil, observes every byte written by the MCU (for the
	// logic analyzer).
	OnByte func(byte)

	mu sync.Mutex

	// Configuration (factory defaults from the datasheet).
	mode       byte // 0 TCP server, 1 TCP client, 2 UDP server, 3 UDP client
	ip         [4]byte
	mask       [4]byte
	gw         [4]byte
	localPort  uint16
	targetIP   [4]byte
	targetPort uint16
	baud       uint32
	format     [3]byte
	timeout    uint32
	packetLen  uint32
	clearBuf   bool
	randomPort bool
	dhcp       bool

	frame []byte
	txBuf []byte

	listener  net.Listener
	conn      net.Conn
	udp       *net.UDPConn
	udpPeer   *net.UDPAddr
	connected bool
	gen       uint64

	nextTick uint64
	lastRST  bool
}

// CH9120 modes.
const (
	CH9120TCPServer = 0
	CH9120TCPClient = 1
	CH9120UDPServer = 2
	CH9120UDPClient = 3
)

// NewCH9120 creates a module attached to the given UART and control pins.
func NewCH9120(chip *rp2350.Chip, uart, cfg0, rst, tcpcs int) *CH9120 {
	d := &CH9120{
		Chip:  chip,
		UART:  uart,
		CFG0:  cfg0,
		RST:   rst,
		TCPCS: tcpcs,
	}
	d.setDefaults()
	return d
}

func (d *CH9120) setDefaults() {
	d.mode = CH9120TCPClient
	d.ip = [4]byte{192, 168, 1, 200}
	d.mask = [4]byte{255, 255, 255, 0}
	d.gw = [4]byte{192, 168, 1, 1}
	d.localPort = 2000
	d.targetIP = [4]byte{192, 168, 1, 100}
	d.targetPort = 1000
	d.baud = 9600
	d.format = [3]byte{1, 4, 8}
	d.timeout = 0
	d.packetLen = 512
	d.clearBuf = false
	d.randomPort = false
	d.dhcp = false
}

// Attach hooks the module into the chip: UART bytes are fed to the command
// parser or the transparent data path, and a cycle hook services the network.
func (d *CH9120) Attach() {
	c := d.Chip
	// The module owns the UART: replacing the console tap keeps configuration
	// commands and socket payloads out of the firmware's console.
	if d.UART == 0 {
		c.OnUART0 = d.ReceiveByte
	} else {
		c.OnUART1 = d.ReceiveByte
	}
	c.CycleHooks = append(c.CycleHooks, d.Tick)
}

func (d *CH9120) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

// ReceiveByte consumes one byte written by the MCU.
func (d *CH9120) ReceiveByte(b byte) {
	if d.OnByte != nil {
		d.OnByte(b)
	}
	d.mu.Lock()
	if d.inConfigMode() {
		d.frame = append(d.frame, b)
		for {
			if len(d.frame) < 3 {
				break
			}
			if d.frame[0] != 0x57 || d.frame[1] != 0xab {
				d.frame = d.frame[1:]
				continue
			}
			need := 3 + ch9120ParamLen(d.frame[2])
			if len(d.frame) < need {
				break
			}
			cmd := d.frame[2]
			params := append([]byte(nil), d.frame[3:need]...)
			d.frame = d.frame[need:]
			d.handleCommand(cmd, params)
		}
		d.mu.Unlock()
		return
	}
	d.txBuf = append(d.txBuf, b)
	d.mu.Unlock()
}

func (d *CH9120) inConfigMode() bool {
	if d.CFG0 < 0 {
		return false
	}
	return !d.Chip.Pins.Level(d.CFG0)
}

func ch9120ParamLen(cmd byte) int {
	switch cmd {
	case 0x10, 0x17, 0x24, 0x26, 0x33:
		return 1
	case 0x14, 0x16:
		return 2
	case 0x22:
		return 3
	case 0x11, 0x12, 0x13, 0x15, 0x21, 0x23, 0x25:
		return 4
	default:
		return 0
	}
}

func (d *CH9120) handleCommand(cmd byte, p []byte) {
	switch cmd {
	case 0x01: // version
		d.send([]byte{0x01, 0x00})
		return
	case 0x02: // reset: reload the saved parameters (modelled as the current
		// configuration) and reopen the socket.
		d.stopNetworkLocked()
		d.applyLocked()
		d.send([]byte{0xaa})
	case 0x03: // TCP connection status
		if d.connected {
			d.send([]byte{0x01})
		} else {
			d.send([]byte{0x00})
		}
	case 0x0d: // save
		d.send([]byte{0xaa})
	case 0x0e: // apply and reset
		d.applyLocked()
		d.send([]byte{0xaa})
	case 0x5e: // exit configuration
		d.send([]byte{0xaa})
	case 0x10:
		d.mode = p[0] & 3
		d.send([]byte{0xaa})
	case 0x11:
		copy(d.ip[:], p)
		d.send([]byte{0xaa})
	case 0x12:
		copy(d.mask[:], p)
		d.send([]byte{0xaa})
	case 0x13:
		copy(d.gw[:], p)
		d.send([]byte{0xaa})
	case 0x14:
		d.localPort = uint16(p[0])<<8 | uint16(p[1])
		d.send([]byte{0xaa})
	case 0x15:
		copy(d.targetIP[:], p)
		d.send([]byte{0xaa})
	case 0x16:
		d.targetPort = uint16(p[0])<<8 | uint16(p[1])
		d.send([]byte{0xaa})
	case 0x17:
		d.randomPort = p[0] != 0
		d.send([]byte{0xaa})
	case 0x21:
		d.baud = uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16 | uint32(p[3])<<24
		d.send([]byte{0xaa})
	case 0x22:
		copy(d.format[:], p)
		d.send([]byte{0xaa})
	case 0x23:
		d.timeout = uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16 | uint32(p[3])<<24
		d.send([]byte{0xaa})
	case 0x24:
		d.send([]byte{0xaa})
	case 0x25:
		d.packetLen = uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16 | uint32(p[3])<<24
		d.send([]byte{0xaa})
	case 0x26:
		d.clearBuf = p[0] != 0
		d.send([]byte{0xaa})
	case 0x33:
		d.dhcp = p[0] != 0
		d.send([]byte{0xaa})
	case 0x60:
		d.send([]byte{d.mode})
	case 0x61:
		d.send(d.ip[:])
	case 0x62:
		d.send(d.mask[:])
	case 0x63:
		d.send(d.gw[:])
	case 0x64:
		d.send([]byte{byte(d.localPort >> 8), byte(d.localPort)})
	case 0x65:
		d.send(d.targetIP[:])
	case 0x66:
		d.send([]byte{byte(d.targetPort >> 8), byte(d.targetPort)})
	case 0x71:
		d.send([]byte{byte(d.baud), byte(d.baud >> 8), byte(d.baud >> 16), byte(d.baud >> 24)})
	case 0x72:
		d.send(d.format[:])
	case 0x73:
		d.send([]byte{byte(d.timeout), byte(d.timeout >> 8), byte(d.timeout >> 16), byte(d.timeout >> 24)})
	case 0x74:
		d.send([]byte{0})
	case 0x75:
		d.send([]byte{byte(d.packetLen), byte(d.packetLen >> 8), byte(d.packetLen >> 16), byte(d.packetLen >> 24)})
	case 0x76:
		if d.clearBuf {
			d.send([]byte{1})
		} else {
			d.send([]byte{0})
		}
	case 0x81:
		d.send([]byte{0x84, 0xc2, 0xe4, 0x05, 0x06, 0x07})
	default:
		d.send([]byte{0xaa})
	}
}

func (d *CH9120) send(b []byte) {
	if d.ReplyTap != nil {
		d.ReplyTap(b)
	}
	d.Chip.SendUART(d.UART, b)
}

// Tick services the module once per millisecond of virtual time.
func (d *CH9120) Tick() {
	now := d.Chip.Cycles
	if now < d.nextTick {
		return
	}
	d.nextTick = now + 150_000 // 1 ms at 150 MHz

	d.mu.Lock()
	rst := d.RST >= 0 && !d.Chip.Pins.Level(d.RST)
	if rst && !d.lastRST {
		d.stopNetworkLocked()
		d.applyLocked()
		d.logf("ch9120: reset")
	}
	d.lastRST = rst

	if len(d.txBuf) > 0 && d.connected {
		data := d.txBuf
		d.txBuf = nil
		if d.udp != nil && d.udpPeer != nil {
			if n, err := d.udp.WriteToUDP(data, d.udpPeer); err != nil {
				d.logf("ch9120: udp send: %v", err)
			} else {
				d.logf("ch9120: tx udp %d bytes", n)
			}
		} else if d.conn != nil {
			if n, err := d.conn.Write(data); err != nil {
				d.logf("ch9120: tcp send: %v", err)
			} else {
				d.logf("ch9120: tx tcp %d bytes", n)
			}
		}
	} else if len(d.txBuf) > 64*1024 {
		d.txBuf = d.txBuf[len(d.txBuf)-32*1024:]
	}

	if d.TCPCS >= 0 {
		d.Chip.Pins.SetExternal(d.TCPCS, !d.connected)
	}
	d.mu.Unlock()
}

// applyLocked (re)opens the socket using the current configuration. The
// network work runs in goroutines so the emulator never blocks.
func (d *CH9120) applyLocked() {
	d.stopNetworkLocked()
	gen := d.gen
	addr := net.JoinHostPort(net.IP(d.targetIP[:]).String(), strconv.Itoa(int(d.targetPort)))
	mode := d.mode
	port := d.localPort
	d.logf("ch9120: mode=%d local=%d target=%s", mode, port, addr)

	switch mode {
	case CH9120TCPServer:
		go d.serveTCP(gen, port)
	case CH9120TCPClient:
		go d.dialTCP(gen, addr)
	case CH9120UDPServer:
		go d.serveUDP(gen, port)
	case CH9120UDPClient:
		go d.dialUDP(gen, addr)
	}
}

func (d *CH9120) serveTCP(gen uint64, port uint16) {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(int(port)))
	if err != nil {
		d.logf("ch9120: listen :%d: %v", port, err)
		return
	}
	d.mu.Lock()
	if d.gen != gen {
		d.mu.Unlock()
		ln.Close()
		return
	}
	d.listener = ln
	d.mu.Unlock()
	d.logf("ch9120: listening on :%d", port)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		d.mu.Lock()
		if d.gen != gen {
			d.mu.Unlock()
			conn.Close()
			return
		}
		if d.conn != nil {
			d.conn.Close()
		}
		d.conn = conn
		d.connected = true
		d.mu.Unlock()
		d.logf("ch9120: client %s", conn.RemoteAddr())
		d.readTCP(gen, conn)
	}
}

func (d *CH9120) dialTCP(gen uint64, addr string) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		d.logf("ch9120: dial %s: %v", addr, err)
		return
	}
	d.mu.Lock()
	if d.gen != gen {
		d.mu.Unlock()
		conn.Close()
		return
	}
	d.conn = conn
	d.connected = true
	d.mu.Unlock()
	d.logf("ch9120: connected to %s", addr)
	d.readTCP(gen, conn)
}

func (d *CH9120) readTCP(gen uint64, conn net.Conn) {
	buf := make([]byte, 2048)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			d.mu.Lock()
			if d.gen == gen {
				d.send(append([]byte(nil), buf[:n]...))
				d.logf("ch9120: rx tcp %d bytes", n)
			}
			d.mu.Unlock()
		}
		if err != nil {
			d.mu.Lock()
			if d.gen == gen {
				d.connected = false
				d.conn = nil
			}
			d.mu.Unlock()
			conn.Close()
			return
		}
	}
}

func (d *CH9120) serveUDP(gen uint64, port uint16) {
	addr, err := net.ResolveUDPAddr("udp", ":"+strconv.Itoa(int(port)))
	if err != nil {
		return
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		d.logf("ch9120: udp listen :%d: %v", port, err)
		return
	}
	d.mu.Lock()
	if d.gen != gen {
		d.mu.Unlock()
		conn.Close()
		return
	}
	d.udp = conn
	d.connected = true
	d.mu.Unlock()
	d.logf("ch9120: udp listening on :%d", port)
	buf := make([]byte, 2048)
	for {
		n, peer, err := conn.ReadFromUDP(buf)
		if n > 0 {
			d.mu.Lock()
			if d.gen == gen {
				d.udpPeer = peer
				d.send(append([]byte(nil), buf[:n]...))
				d.logf("ch9120: rx udp %d bytes", n)
			}
			d.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (d *CH9120) dialUDP(gen uint64, addr string) {
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		d.logf("ch9120: udp dial %s: %v", addr, err)
		return
	}
	d.mu.Lock()
	if d.gen != gen {
		d.mu.Unlock()
		conn.Close()
		return
	}
	d.udp = conn
	d.udpPeer = raddr
	d.connected = true
	d.mu.Unlock()
	d.logf("ch9120: udp connected to %s", addr)
	buf := make([]byte, 2048)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			d.mu.Lock()
			if d.gen == gen {
				d.send(append([]byte(nil), buf[:n]...))
				d.logf("ch9120: rx udp %d bytes", n)
			}
			d.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (d *CH9120) stopNetworkLocked() {
	d.gen++
	if d.conn != nil {
		d.conn.Close()
		d.conn = nil
	}
	if d.listener != nil {
		d.listener.Close()
		d.listener = nil
	}
	if d.udp != nil {
		d.udp.Close()
		d.udp = nil
	}
	d.udpPeer = nil
	d.connected = false
	d.txBuf = nil
}

// Status describes the module for the UI.
func (d *CH9120) Status() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	mode := []string{"tcp-server", "tcp-client", "udp-server", "udp-client"}[d.mode&3]
	state := "down"
	if d.connected {
		state = "up"
	}
	return fmt.Sprintf("%s · local %d · %s:%d · %s", mode, d.localPort, net.IP(d.targetIP[:]), d.targetPort, state)
}
