package devices

import (
	"bytes"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestEthernetWriteDeadline(t *testing.T) {
	for _, kind := range []string{"w5500", "w6300"} {
		t.Run(kind, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			accepted := make(chan net.Conn, 1)
			go func() {
				conn, err := ln.Accept()
				if err == nil {
					accepted <- conn
				}
			}()
			client, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			var peer net.Conn
			select {
			case peer = <-accepted:
			case <-time.After(2 * time.Second):
				t.Fatal("peer was not accepted")
			}
			defer peer.Close()
			if tcp, ok := peer.(*net.TCPConn); ok {
				if err := tcp.SetWriteBuffer(1024); err != nil {
					t.Fatal(err)
				}
			}

			payload := bytes.Repeat([]byte{0x5a}, 16*1024)
			var send func()
			var timedOut func() bool
			if kind == "w5500" {
				w, _ := newTestW5500()
				w.WriteTimeout = 50 * time.Millisecond
				t.Cleanup(w.Close)
				send = func() {
					w.mu.Lock()
					defer w.mu.Unlock()
					s := w.socks[0]
					s.proto, s.status, s.conn = 1, w5StatusEstablished, peer
					s.txReadPtr, s.txWritePtr = 0, uint16(len(payload))
					copy(w.txMem[0], payload)
					w.send(0)
				}
				timedOut = func() bool {
					w.mu.Lock()
					defer w.mu.Unlock()
					return w.socks[0].ints&w5IntTimeout != 0
				}
			} else {
				w, _ := newTestW6300()
				w.WriteTimeout = 50 * time.Millisecond
				t.Cleanup(w.Close)
				send = func() {
					w.mu.Lock()
					defer w.mu.Unlock()
					s := w.socks[0]
					s.proto, s.status, s.conn = 1, w6StatusEstablished, peer
					s.txReadPtr, s.txWritePtr = 0, uint16(len(payload))
					copy(w.txMem[0], payload)
					w.send(0)
				}
				timedOut = func() bool {
					w.mu.Lock()
					defer w.mu.Unlock()
					return w.socks[0].irq&w6IrTimeout != 0
				}
			}

			start := time.Now()
			for range 128 {
				send()
				if timedOut() {
					break
				}
			}
			if !timedOut() {
				t.Fatal("non-reading peer did not produce a write timeout")
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("write deadline returned after %v", elapsed)
			}
		})
	}
}

func TestCH9120WriteDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var peer net.Conn
	select {
	case peer = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("peer was not accepted")
	}
	defer peer.Close()
	if tcp, ok := peer.(*net.TCPConn); ok {
		if err := tcp.SetWriteBuffer(1024); err != nil {
			t.Fatal(err)
		}
	}

	d := NewCH9120(rp2350.New(), 1, -1, -1, -1)
	d.WriteTimeout = 50 * time.Millisecond
	t.Cleanup(d.Close)
	d.mu.Lock()
	d.conn = peer
	d.connected = true
	d.txBuf = bytes.Repeat([]byte{0x33}, 4<<20)
	d.mu.Unlock()
	start := time.Now()
	d.Tick()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("CH9120 write returned after %v", elapsed)
	}
	d.mu.Lock()
	connected := d.connected
	d.mu.Unlock()
	if connected {
		t.Fatal("CH9120 kept a failed socket connected")
	}
}

func TestCH9120DialCancellation(t *testing.T) {
	d := NewCH9120(rp2350.New(), 1, -1, -1, -1)
	t.Cleanup(d.Close)
	d.cancel()
	start := time.Now()
	if conn, err := d.dialContext("tcp", "192.0.2.1:65000"); err == nil {
		_ = conn.Close()
		t.Fatal("canceled dial succeeded")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("canceled dial returned after %v", elapsed)
	}
}

func TestUARTBridgeWriteDeadline(t *testing.T) {
	b := NewUARTBridge(rp2350.New(), 0, "127.0.0.1:0")
	b.WriteTimeout = 50 * time.Millisecond
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	listen, _, _ := b.Status()
	client, err := net.Dial("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	waitFor(t, "UART bridge client", func() bool {
		_, _, connected := b.Status()
		return connected
	})
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	if tcp, ok := conn.(*net.TCPConn); ok {
		if err := tcp.SetWriteBuffer(1024); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	b.Write(bytes.Repeat([]byte{0x44}, 4<<20))
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("emulator-side Write blocked for %v", elapsed)
	}
	waitFor(t, "UART bridge write timeout", func() bool {
		_, _, connected := b.Status()
		return !connected
	})
}

func TestUARTBridgeStartAcceptCloseRace(t *testing.T) {
	port := freeTCPPort(t)
	addr := "127.0.0.1:" + strconv.Itoa(int(port))
	for range 20 {
		b := NewUARTBridge(rp2350.New(), 0, addr)
		if err := b.Start(); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var client net.Conn
		wg.Add(2)
		go func() {
			defer wg.Done()
			client, _ = net.DialTimeout("tcp", addr, time.Second)
		}()
		go func() {
			defer wg.Done()
			b.Close()
		}()
		wg.Wait()
		b.Close()
		if client != nil {
			_ = client.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			var one [1]byte
			if _, err := client.Read(one[:]); err == nil {
				t.Fatal("late UART bridge client stayed connected")
			}
			_ = client.Close()
		}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("fixed port did not survive close/rebind: %v", err)
	}
	_ = ln.Close()
}

func TestBusBridgeStartAcceptCloseRace(t *testing.T) {
	port := freeTCPPort(t)
	addr := "127.0.0.1:" + strconv.Itoa(int(port))
	for range 20 {
		b := NewBusBridge(BusSPI, 0, addr, "")
		b.Hello = "SPI HELLO"
		if err := b.Start(); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var client net.Conn
		wg.Add(2)
		go func() {
			defer wg.Done()
			client, _ = net.DialTimeout("tcp", addr, time.Second)
		}()
		go func() {
			defer wg.Done()
			b.Close()
		}()
		wg.Wait()
		b.Close()
		if client != nil {
			_ = client.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			var one [1]byte
			if _, err := client.Read(one[:]); err == nil {
				t.Fatal("late bus bridge client stayed connected")
			}
			_ = client.Close()
		}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("fixed port did not survive close/rebind: %v", err)
	}
	_ = ln.Close()
}

func TestEthernetListenerCloseRebind(t *testing.T) {
	for _, kind := range []string{"w5500", "w6300"} {
		t.Run(kind, func(t *testing.T) {
			port := freeTCPPort(t)
			var closeDevice func()
			var info func() NetworkInfo
			if kind == "w5500" {
				w, _ := newTestW5500()
				w.mu.Lock()
				w.socks[0].proto = 1
				w.socks[0].srcPort = port
				w.listen(0)
				w.mu.Unlock()
				closeDevice, info = w.Close, w.NetworkInfo
			} else {
				w, _ := newTestW6300()
				w.mu.Lock()
				w.socks[0].proto = 1
				w.socks[0].srcPort = port
				w.listen(0)
				w.mu.Unlock()
				closeDevice, info = w.Close, w.NetworkInfo
			}
			t.Cleanup(closeDevice)
			waitFor(t, kind+" listener", func() bool {
				sockets := info().Sockets
				return len(sockets) == 1 && sockets[0].HostAddr != ""
			})
			closeDevice()
			ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(int(port)))
			if err != nil {
				t.Fatalf("listener port still owned after close: %v", err)
			}
			_ = ln.Close()
		})
	}
}

func TestCH9120ServerCloseRebind(t *testing.T) {
	port := freeTCPPort(t)
	addr := "127.0.0.1:" + strconv.Itoa(int(port))
	d := NewCH9120(rp2350.New(), 1, -1, -1, -1)
	t.Cleanup(d.Close)
	d.mu.Lock()
	d.mode = CH9120TCPServer
	d.localPort = port
	d.applyLocked()
	d.mu.Unlock()
	waitFor(t, "CH9120 listener", func() bool {
		sockets := d.NetworkInfo().Sockets
		return len(sockets) == 1 && sockets[0].HostAddr != ""
	})

	var wg sync.WaitGroup
	var client net.Conn
	wg.Add(2)
	go func() {
		defer wg.Done()
		client, _ = net.DialTimeout("tcp", addr, time.Second)
	}()
	go func() {
		defer wg.Done()
		d.Close()
	}()
	wg.Wait()
	d.Close()
	if client != nil {
		_ = client.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		var one [1]byte
		if _, err := client.Read(one[:]); err == nil {
			t.Fatal("late CH9120 client stayed connected")
		}
		_ = client.Close()
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("CH9120 port still owned after close: %v", err)
	}
	_ = ln.Close()
}
