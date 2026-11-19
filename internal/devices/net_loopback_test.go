package devices

import (
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

// These tests drive the Ethernet models through their register interfaces
// against real host sockets. They cover the flows TinyGo's netdev drivers use
// (Open, Connect/Listen, Send, Recv, Accept and listener re-arm) so the host
// networking paths stay covered on every platform.

func waitByte(t *testing.T, read func() byte, want byte) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if read() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("register = %#x, want %#x", read(), want)
}

func freeTCPPort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	return port
}

// W5500 block-select encoding for socket n.
func w5bsb(n int, kind uint8) uint8 { return uint8(n)<<2 | kind }

func TestW5500TCPClientRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	peer := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			peer <- conn
		}
	}()

	w, c := newTestW5500()
	sw := func(addr uint16, data ...byte) { w.writeReg(c, addr, w5bsb(0, 1), data...) }
	sr := func(addr uint16, n int) []byte { return w.readReg(c, addr, w5bsb(0, 1), n) }

	sw(w5SockMode, 1) // TCP
	sw(w5SockDestIP, 127, 0, 0, 1)
	sw(w5SockDestPort, byte(port>>8), byte(port))
	sw(w5SockCmd, w5CmdOpen)

	waitByte(t, func() byte { return sr(w5SockStatus, 1)[0] }, w5StatusEstablished)

	var conn net.Conn
	select {
	case conn = <-peer:
	case <-time.After(5 * time.Second):
		t.Fatal("host never accepted the emulated client")
	}
	defer func() { _ = conn.Close() }()

	// Send: write the TX buffer at TX_WR and issue SEND.
	w.writeReg(c, 0, w5bsb(0, 2), 'a', 'b', 'c')
	sw(w5SockTXWritePtr, 0, 3)
	sw(w5SockCmd, w5CmdSend)
	buf := make([]byte, 3)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("host read: %v", err)
	}
	if string(buf) != "abc" {
		t.Fatalf("host read %q, want %q", buf, "abc")
	}
	waitByte(t, func() byte { return sr(w5SockInt, 1)[0] & w5IntSendOK }, w5IntSendOK)

	// Receive: the host writes, RX_RSR exposes it, RECV clears it.
	if _, err := conn.Write([]byte("def")); err != nil {
		t.Fatalf("host write: %v", err)
	}
	waitByte(t, func() byte { return sr(w5SockInt, 1)[0] & w5IntReceive }, w5IntReceive)
	if got := sr(w5SockRXRecvSize, 2); int(got[0])<<8|int(got[1]) != 3 {
		t.Fatalf("RX size = % x, want 3", got)
	}
	if got := w.readReg(c, 0, w5bsb(0, 3), 3); string(got) != "def" {
		t.Fatalf("RX buffer = %q, want %q", got, "def")
	}
	sw(w5SockRXReadPtr, 0, 3)
	sw(w5SockCmd, w5CmdRecv)
	if got := sr(w5SockRXRecvSize, 2); got[0] != 0 || got[1] != 0 {
		t.Fatalf("RX size after RECV = % x, want 0", got)
	}
}

// TestW5500ListenerRearm mirrors TinyGo's server flow: after an accept the
// driver swaps hardware sockets, reopens and re-listens on the local port, so
// a second peer must be accepted through the same listening descriptor.
func TestW5500ListenerRearm(t *testing.T) {
	port := freeTCPPort(t)
	w, c := newTestW5500()
	sw := func(n int, addr uint16, data ...byte) {
		w.writeReg(c, addr, w5bsb(n, 1), data...)
	}
	sr := func(n int, addr uint16, count int) []byte {
		return w.readReg(c, addr, w5bsb(n, 1), count)
	}

	// Socket 0 listens on port.
	sw(0, w5SockMode, 1)
	sw(0, w5SockSrcPort, byte(port>>8), byte(port))
	sw(0, w5SockCmd, w5CmdOpen)
	sw(0, w5SockCmd, w5CmdListen)
	waitByte(t, func() byte { return sr(0, w5SockStatus, 1)[0] }, w5StatusListen)

	first, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(int(port)))
	if err != nil {
		t.Fatalf("dial first: %v", err)
	}
	defer func() { _ = first.Close() }()
	waitByte(t, func() byte { return sr(0, w5SockStatus, 1)[0] }, w5StatusEstablished)

	// The driver hands the established socket to the caller and moves the
	// listener to hardware socket 1.
	sw(1, w5SockMode, 1)
	sw(1, w5SockSrcPort, byte(port>>8), byte(port))
	sw(1, w5SockCmd, w5CmdOpen)
	if got := sr(1, w5SockStatus, 1)[0]; got != w5StatusInit {
		t.Fatalf("re-armed socket status = %#x, want init (%#x)", got, w5StatusInit)
	}
	sw(1, w5SockCmd, w5CmdListen)
	if got := sr(1, w5SockStatus, 1)[0]; got != w5StatusListen {
		t.Fatalf("re-armed listener status = %#x, want listen (%#x)", got, w5StatusListen)
	}

	second, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(int(port)))
	if err != nil {
		t.Fatalf("dial after re-arm: %v", err)
	}
	defer func() { _ = second.Close() }()
	waitByte(t, func() byte { return sr(1, w5SockStatus, 1)[0] }, w5StatusEstablished)
}

// TestW5500RingWrap pushes more than one buffer's worth of data through each
// direction so the absolute pointers and the modulo indexing are exercised.
func TestW5500RingWrap(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	peer := make(chan net.Conn, 1)
	go func() {
		if conn, err := ln.Accept(); err == nil {
			peer <- conn
		}
	}()

	w, c := newTestW5500()
	sw := func(addr uint16, data ...byte) { w.writeReg(c, addr, w5bsb(0, 1), data...) }
	sr := func(addr uint16, n int) []byte { return w.readReg(c, addr, w5bsb(0, 1), n) }

	sw(w5SockRXBufSize, 4) // 4 KB rings
	sw(w5SockTXBufSize, 4)
	sw(w5SockMode, 1)
	sw(w5SockDestIP, 127, 0, 0, 1)
	sw(w5SockDestPort, byte(port>>8), byte(port))
	sw(w5SockCmd, w5CmdOpen)
	waitByte(t, func() byte { return sr(w5SockStatus, 1)[0] }, w5StatusEstablished)

	var conn net.Conn
	select {
	case conn = <-peer:
	case <-time.After(5 * time.Second):
		t.Fatal("host never accepted the emulated client")
	}
	defer func() { _ = conn.Close() }()

	// TX: two 3 KB chunks, the second wraps the 4 KB ring.
	const chunk = 3000
	tx := make([]byte, 0, 2*chunk)
	for i := range 2 * chunk {
		tx = append(tx, byte(i*7))
	}
	for i := range 2 {
		w.writeReg(c, uint16(i*chunk), w5bsb(0, 2), tx[i*chunk:(i+1)*chunk]...)
		w.writeReg(c, w5SockTXWritePtr, w5bsb(0, 1), byte((i+1)*chunk>>8), byte((i+1)*chunk))
		sw(w5SockCmd, w5CmdSend)
		waitByte(t, func() byte { return sr(w5SockInt, 1)[0] & w5IntSendOK }, w5IntSendOK)
	}
	got := make([]byte, 2*chunk)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("host read: %v", err)
	}
	for i := range got {
		if got[i] != tx[i] {
			t.Fatalf("TX byte %d = %#x, want %#x", i, got[i], tx[i])
		}
	}

	// RX: two 3 KB chunks, the second wrapping.
	for i := range 2 {
		if _, err := conn.Write(tx[i*chunk : (i+1)*chunk]); err != nil {
			t.Fatalf("host write: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if n := sr(w5SockRXRecvSize, 2); int(n[0])<<8|int(n[1]) >= chunk {
				break
			}
			time.Sleep(time.Millisecond)
		}
		rd := uint16(i * chunk)
		got := w.readReg(c, rd, w5bsb(0, 3), chunk)
		for j := range got {
			if got[j] != tx[i*chunk+j] {
				t.Fatalf("RX chunk %d byte %d = %#x, want %#x", i, j, got[j], tx[i*chunk+j])
			}
		}
		want := (i + 1) * chunk
		w.writeReg(c, w5SockRXReadPtr, w5bsb(0, 1), byte(want>>8), byte(want))
		sw(w5SockCmd, w5CmdRecv)
	}
}

// TestW6300RingWrap is the W6300 counterpart of the W5500 wrap test: the
// controller has one TX and one RX ring per socket and the driver addresses
// them with absolute pointers.
func TestW6300RingWrap(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	peer := make(chan net.Conn, 1)
	go func() {
		if conn, err := ln.Accept(); err == nil {
			peer <- conn
		}
	}()

	w, c := newTestW6300()
	sw := func(addr uint16, data ...byte) { w.writeBlock(c, w6BlockRegs, addr, data...) }
	sr := func(addr uint16, n int) []byte { return w.readBlock(c, w6BlockRegs, addr, n) }

	sw(w6SnMR, 1)
	sw(w6SnDIPR, 127, 0, 0, 1)
	sw(w6SnDPORTR, byte(port>>8), byte(port))
	sw(w6SnCR, w6CmdOpen)
	sw(w6SnCR, w6CmdConnect)
	waitByte(t, func() byte { return sr(w6SnSR, 1)[0] }, w6StatusEstablished)

	var conn net.Conn
	select {
	case conn = <-peer:
	case <-time.After(5 * time.Second):
		t.Fatal("host never accepted the emulated client")
	}
	defer func() { _ = conn.Close() }()

	const chunk = 3000
	tx := make([]byte, 0, 2*chunk)
	for i := range 2 * chunk {
		tx = append(tx, byte(i*7))
	}
	for i := range 2 {
		w.writeBlock(c, w6BlockTX, uint16(i*chunk), tx[i*chunk:(i+1)*chunk]...)
		sw(w6SnTXWR, byte((i+1)*chunk>>8), byte((i+1)*chunk))
		sw(w6SnCR, w6CmdSend)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			irq := sr(w6SnIR, 1)[0]
			if irq&w6IrSendOK != 0 || irq&w6IrTimeout != 0 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if irq := sr(w6SnIR, 1)[0]; irq&w6IrSendOK == 0 {
			t.Fatalf("chunk %d send irq = %#x", i, irq)
		}
		sw(w6SnIRCLR, w6IrSendOK)
	}
	got := make([]byte, 2*chunk)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("host read: %v", err)
	}
	for i := range got {
		if got[i] != tx[i] {
			t.Fatalf("TX byte %d = %#x, want %#x", i, got[i], tx[i])
		}
	}

	for i := range 2 {
		if _, err := conn.Write(tx[i*chunk : (i+1)*chunk]); err != nil {
			t.Fatalf("host write: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			n := sr(w6SnRXRSR, 2)
			if int(n[0])<<8|int(n[1]) >= chunk {
				break
			}
			time.Sleep(time.Millisecond)
		}
		got := w.readBlock(c, w6BlockRX, uint16(i*chunk), chunk)
		for j := range got {
			if got[j] != tx[i*chunk+j] {
				t.Fatalf("RX chunk %d byte %d = %#x, want %#x", i, j, got[j], tx[i*chunk+j])
			}
		}
		want := (i + 1) * chunk
		sw(w6SnRXRD, byte(want>>8), byte(want))
		sw(w6SnCR, w6CmdRecv)
	}
}
