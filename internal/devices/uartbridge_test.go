package devices

import (
	"net"
	"testing"
	"time"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func TestUARTBridgeDeliversHostBurst(t *testing.T) {
	chip := rp2350.New()
	bridge := NewUARTBridge(chip, 1, "127.0.0.1:0")
	if err := bridge.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Close)

	listen, _, _ := bridge.Status()
	conn, err := net.DialTimeout("tcp", listen, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	waitFor(t, "UART bridge client", func() bool {
		_, _, connected := bridge.Status()
		return connected
	})

	payload := []byte("serial-check")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	const base = 0x40078000
	deadline := time.Now().Add(2 * time.Second)
	for _, want := range payload {
		for chip.Read32(base+0x18)&(1<<4) != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for UART byte %#x", want)
			}
			time.Sleep(time.Millisecond)
		}
		if got := byte(chip.Read32(base)); got != want {
			t.Fatalf("UART byte = %#x, want %#x", got, want)
		}
	}
}
