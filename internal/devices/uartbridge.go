package devices

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// UARTBridge exposes one emulated UART over a TCP listener: bytes from the
// socket enter the UART receive FIFO, and bytes the firmware transmits are
// forwarded to the connected client. It lets a terminal program talk to the
// firmware as if it were wired to the UART.
type UARTBridge struct {
	Chip       *rp2350.Chip
	UART       int
	ListenAddr string

	// Logger, when non-nil, reports listener and client events.
	Logger *slog.Logger
	// WriteTimeout bounds writes to a non-reading client. Zero uses 2 seconds.
	WriteTimeout time.Duration

	lifecycle sync.Mutex
	mu        sync.Mutex
	ln        net.Listener
	conn      net.Conn
	client    string
	started   bool
	closed    bool

	tx   chan []byte
	done chan struct{}
	wg   sync.WaitGroup
}

// NewUARTBridge creates a bridge for UART 0 or 1 on the given TCP address.
// An empty address listens on 127.0.0.1:0, so the kernel picks an available
// port; Status reports the assigned address.
func NewUARTBridge(chip *rp2350.Chip, uart int, listen string) *UARTBridge {
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	return &UARTBridge{
		Chip:       chip,
		UART:       uart,
		ListenAddr: listen,
		tx:         make(chan []byte, 4096),
		done:       make(chan struct{}),
	}
}

// Start opens the listener and the pump goroutines.
func (b *UARTBridge) Start() error {
	if b.UART < 0 || b.UART > 1 {
		return fmt.Errorf("uart bridge: invalid UART %d", b.UART)
	}
	b.lifecycle.Lock()
	defer b.lifecycle.Unlock()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return fmt.Errorf("uart bridge: closed")
	}
	if b.started {
		b.mu.Unlock()
		return fmt.Errorf("uart bridge: already started")
	}
	if b.tx == nil {
		b.tx = make(chan []byte, 4096)
	}
	if b.done == nil {
		b.done = make(chan struct{})
	}
	b.started = true
	tx, done := b.tx, b.done
	b.mu.Unlock()

	ln, err := net.Listen("tcp", b.ListenAddr)
	if err != nil {
		b.mu.Lock()
		b.started = false
		b.mu.Unlock()
		return err
	}
	b.mu.Lock()
	b.ln = ln
	b.mu.Unlock()
	b.wg.Go(func() { b.acceptLoop(ln) })
	b.wg.Go(func() { b.txLoop(tx, done) })
	loggerOr(b.Logger).Info("uart bridge listening", slog.Int("uart", b.UART), slog.String("addr", ln.Addr().String()))
	return nil
}

func (b *UARTBridge) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		if b.closed || b.ln != ln {
			b.mu.Unlock()
			_ = conn.Close()
			return
		}
		old := b.conn
		b.conn = conn
		b.client = conn.RemoteAddr().String()
		b.wg.Go(func() { b.readLoop(conn) })
		b.mu.Unlock()
		if old != nil {
			_ = old.Close()
		}
		loggerOr(b.Logger).Info("uart bridge client", slog.Int("uart", b.UART), slog.String("remote", conn.RemoteAddr().String()))
	}
}

func (b *UARTBridge) chip() *rp2350.Chip {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Chip
}

// SetChip points the bridge at a new chip after a firmware reset while keeping
// the listener and client connection alive.
func (b *UARTBridge) SetChip(chip *rp2350.Chip) {
	b.mu.Lock()
	b.Chip = chip
	b.mu.Unlock()
}

func (b *UARTBridge) readLoop(conn net.Conn) {
	buf := make([]byte, 256)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if chip := b.chip(); chip != nil {
				chip.SendUART(b.UART, buf[:n])
			}
		}
		if err != nil {
			b.dropClient(conn)
			loggerOr(b.Logger).Info("uart bridge client gone", slog.Int("uart", b.UART))
			return
		}
	}
}

func (b *UARTBridge) dropClient(conn net.Conn) {
	_ = conn.Close()
	b.mu.Lock()
	if b.conn == conn {
		b.conn = nil
		b.client = ""
	}
	b.mu.Unlock()
}

func (b *UARTBridge) txLoop(tx <-chan []byte, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		default:
		}
		select {
		case data := <-tx:
			b.mu.Lock()
			conn := b.conn
			timeout := b.WriteTimeout
			b.mu.Unlock()
			if conn == nil {
				continue
			}
			if _, err := writeNet(conn, data, timeout); err != nil {
				b.dropClient(conn)
			}
		case <-done:
			return
		}
	}
}

// Write queues firmware bytes for the connected client. It never blocks the
// emulator; data is dropped when no client is connected or the queue is full.
func (b *UARTBridge) Write(data []byte) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	tx, done := b.tx, b.done
	b.mu.Unlock()
	if tx == nil {
		return
	}
	select {
	case tx <- append([]byte(nil), data...):
	case <-done:
	default:
	}
}

// Status describes the listener and the current client.
func (b *UARTBridge) Status() (listen, client string, connected bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ln != nil {
		listen = b.ln.Addr().String()
	} else {
		listen = b.ListenAddr
	}
	return listen, b.client, b.conn != nil
}

// Close stops the listener, disconnects the client and joins every pump.
func (b *UARTBridge) Close() {
	b.lifecycle.Lock()
	defer b.lifecycle.Unlock()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		b.wg.Wait()
		return
	}
	b.closed = true
	b.started = false
	ln, conn, done := b.ln, b.conn, b.done
	b.ln = nil
	b.conn = nil
	b.client = ""
	b.mu.Unlock()
	if done != nil {
		close(done)
	}
	if conn != nil {
		_ = conn.Close()
	}
	if ln != nil {
		_ = ln.Close()
	}
	b.wg.Wait()
}
