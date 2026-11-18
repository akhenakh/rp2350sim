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

	mu     sync.Mutex
	ln     net.Listener
	conn   net.Conn
	client string

	tx        chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

// NewUARTBridge creates a bridge for UART 0 or 1 on the given TCP address.
// An empty address listens on 127.0.0.1:0, so the kernel picks an available
// port; Status reports the assigned address.
func NewUARTBridge(chip *rp2350.Chip, uart int, listen string) *UARTBridge {
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	return &UARTBridge{Chip: chip, UART: uart, ListenAddr: listen}
}

// Start opens the listener and the pump goroutines.
func (b *UARTBridge) Start() error {
	if b.UART < 0 || b.UART > 1 {
		return fmt.Errorf("uart bridge: invalid UART %d", b.UART)
	}
	ln, err := net.Listen("tcp", b.ListenAddr)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.ln = ln
	b.mu.Unlock()
	b.tx = make(chan []byte, 4096)
	b.done = make(chan struct{})
	go b.acceptLoop()
	go b.txLoop()
	loggerOr(b.Logger).Info("uart bridge listening", slog.Int("uart", b.UART), slog.String("addr", ln.Addr().String()))
	return nil
}

func (b *UARTBridge) acceptLoop() {
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		if b.conn != nil {
			_ = b.conn.Close()
		}
		b.conn = conn
		b.client = conn.RemoteAddr().String()
		b.mu.Unlock()
		loggerOr(b.Logger).Info("uart bridge client", slog.Int("uart", b.UART), slog.String("remote", conn.RemoteAddr().String()))
		go b.readLoop(conn)
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
			b.mu.Lock()
			if b.conn == conn {
				b.conn = nil
				b.client = ""
			}
			b.mu.Unlock()
			loggerOr(b.Logger).Info("uart bridge client gone", slog.Int("uart", b.UART))
			return
		}
	}
}

func (b *UARTBridge) txLoop() {
	for {
		select {
		case data := <-b.tx:
			b.mu.Lock()
			conn := b.conn
			b.mu.Unlock()
			if conn == nil {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if _, err := conn.Write(data); err != nil {
				_ = conn.Close()
				b.mu.Lock()
				if b.conn == conn {
					b.conn = nil
					b.client = ""
				}
				b.mu.Unlock()
			}
		case <-b.done:
			return
		}
	}
}

// Write queues firmware bytes for the connected client. It never blocks the
// emulator; data is dropped when no client is connected or the queue is full.
func (b *UARTBridge) Write(data []byte) {
	if b.tx == nil {
		return
	}
	select {
	case b.tx <- append([]byte(nil), data...):
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

// Close stops the listener and disconnects the client.
func (b *UARTBridge) Close() {
	b.closeOnce.Do(func() {
		if b.done != nil {
			close(b.done)
		}
		b.mu.Lock()
		if b.conn != nil {
			_ = b.conn.Close()
			b.conn = nil
		}
		if b.ln != nil {
			_ = b.ln.Close()
			b.ln = nil
		}
		b.mu.Unlock()
	})
}
