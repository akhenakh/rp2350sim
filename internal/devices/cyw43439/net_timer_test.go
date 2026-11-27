//go:build !js

package cyw43439

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akhenakh/rp2350sim/internal/devices"
)

// A gVisor timer is re-armed with Reset after it fires (TCP retransmit,
// delayed ACK, TIME_WAIT). It must rejoin the clock queue, not stay dropped.
func TestVTimerResetRearms(t *testing.T) {
	c := &vclock{}
	fires := make(chan struct{}, 4)
	tm := c.AfterFunc(time.Second, func() { fires <- struct{}{} }).(*vtimer)

	c.advance(time.Second)
	waitFire(t, fires)

	tm.Reset(time.Second)
	c.advance(2 * time.Second)
	waitFire(t, fires)

	// A timer stopped before it fires stays queued until the next advance;
	// Reset must keep it there.
	other := c.AfterFunc(time.Hour, func() { fires <- struct{}{} }).(*vtimer)
	other.Stop()
	other.Reset(time.Second)
	c.advance(3 * time.Second)
	waitFire(t, fires)
}

func waitFire(t *testing.T, fires <-chan struct{}) {
	t.Helper()
	select {
	case <-fires:
	case <-time.After(2 * time.Second):
		t.Fatal("timer did not fire")
	}
}

// stubConn lets a relay test fail one direction while the other stays blocked
// in Read until the relay closes its peer.
type stubConn struct {
	net.Conn
	err    error
	closed chan struct{}
	once   sync.Once
}

func (c *stubConn) Read([]byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	<-c.closed
	return 0, net.ErrClosed
}

func (c *stubConn) Write(p []byte) (int, error) { return len(p), nil }

func (c *stubConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// A one-sided UDP failure must tear down the peer too, or the guest-side
// socket and its natSocket entry stay open forever.
func TestRelayUDPClosesBothOnError(t *testing.T) {
	a := &stubConn{closed: make(chan struct{})}
	b := &stubConn{err: io.EOF, closed: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		relayUDP(a, b)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relayUDP hung after one side failed")
	}
	select {
	case <-a.closed:
	default:
		t.Fatal("relayUDP did not close the idle peer")
	}
}

type stubDataPath struct {
	closed atomic.Int32
}

func (s *stubDataPath) handleFrame([]byte)               {}
func (s *stubDataPath) tick(uint64)                      {}
func (s *stubDataPath) close()                           { s.closed.Add(1) }
func (s *stubDataPath) networkInfo() devices.NetworkInfo { return devices.NetworkInfo{} }

func TestCloseReleasesDataPath(t *testing.T) {
	d := newDevice(&fakeBus{}, Config{})
	dp := &stubDataPath{}
	d.hostNet = dp
	d.Close()
	if got := dp.closed.Load(); got != 1 {
		t.Fatalf("data path closed %d times, want 1", got)
	}
	if d.hostNet != nil {
		t.Fatal("Close left the data path attached")
	}
	d.Close() // a second close must be harmless
	if got := dp.closed.Load(); got != 1 {
		t.Fatalf("second Close called the data path again (%d)", got)
	}
}

// Frames queued from a goroutine (the DNS resolver) must use the virtual clock
// mirror, never Chip.Cycles directly.
func TestCaptureUsesClockMirror(t *testing.T) {
	d := newDevice(&fakeBus{}, Config{Capture: true})
	d.nowMicros.Store(4321)
	d.captureLocked(false, []byte{1, 2, 3})
	if len(d.pcapRing) != 1 {
		t.Fatalf("captured %d frames, want 1", len(d.pcapRing))
	}
	if got := d.pcapRing[0].ts; got != 4321 {
		t.Fatalf("capture timestamp = %d, want the mirrored clock 4321", got)
	}
}
