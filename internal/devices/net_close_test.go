package devices

import (
	"net"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func reserveTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestNetworkControllersCloseListeners(t *testing.T) {
	c := rp2350.New()
	t.Run("w5500", func(t *testing.T) {
		addr := reserveTCPAddr(t)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		w := NewW5500(c, 17)
		w.socks[0].listener = ln
		w.socks[0].pendingTx = []byte{1, 2, 3}
		w.Close()
		w.Close()
		if w.socks[0].listener != nil || w.socks[0].pendingTx != nil {
			t.Fatal("W5500 retained listener or pending transmit data")
		}
		rebound, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("W5500 listener survived Close: %v", err)
		}
		_ = rebound.Close()
	})
	t.Run("w6300", func(t *testing.T) {
		addr := reserveTCPAddr(t)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		w := NewW6300(c, 17)
		w.socks[0].listener = ln
		w.socks[0].pendingTx = []byte{1, 2, 3}
		w.Close()
		w.Close()
		if w.socks[0].listener != nil || w.socks[0].pendingTx != nil {
			t.Fatal("W6300 retained listener or pending transmit data")
		}
		rebound, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("W6300 listener survived Close: %v", err)
		}
		_ = rebound.Close()
	})
	t.Run("ch9120", func(t *testing.T) {
		addr := reserveTCPAddr(t)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		d := NewCH9120(c, 1, 18, 19, 17)
		d.listener = ln
		d.connected = true
		d.Close()
		d.Close()
		if d.listener != nil || d.connected || !d.closed {
			t.Fatal("CH9120 retained listener or connected state")
		}
		rebound, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("CH9120 listener survived Close: %v", err)
		}
		_ = rebound.Close()
	})
}
