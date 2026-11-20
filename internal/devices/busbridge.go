package devices

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// BusKind identifies the wire protocol a BusBridge speaks.
type BusKind string

const (
	BusI2C BusKind = "i2c"
	BusSPI BusKind = "spi"
)

// BusBridge is the transport shared by the I2C and SPI TCP device bridges.
// It either listens for a device program or dials one, and exchanges one
// newline-terminated request/response pair at a time. The chip calls into the
// bridge synchronously, so responses have a deadline; the emulator must never
// hang on a missing or slow device.
type BusBridge struct {
	Kind BusKind
	Bus  int

	// ListenAddr is used when ConnectAddr is empty. An empty value listens on
	// 127.0.0.1:0, so the kernel picks a port and Status reports it.
	ListenAddr string
	// ConnectAddr, when set, makes the bridge dial a device server instead of
	// listening.
	ConnectAddr string

	// Timeout bounds one request/response exchange.
	Timeout time.Duration
	// Wait bounds how long the first bus event waits for a client. After the
	// first attempt the bridge returns an error/NAK immediately, so a run
	// without a device script does not stall on every transaction.
	Wait time.Duration

	// Hello is sent to the device when a connection is established.
	Hello string

	Logger *slog.Logger

	mu        sync.Mutex
	ioMu      sync.Mutex
	ln        net.Listener
	conn      net.Conn
	reader    *bufio.Reader
	client    string
	waited    bool
	closed    bool
	connCh    chan struct{}
	reconnect chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

var (
	errBusNoClient = errors.New("bus bridge: no client")
	errBusClosed   = errors.New("bus bridge: closed")
)

// NewBusBridge creates a bridge for one bus. Exactly one of listen and
// connect may be non-empty; an empty listen defaults to 127.0.0.1:0.
func NewBusBridge(kind BusKind, bus int, listen, connect string) *BusBridge {
	if listen == "" && connect == "" {
		listen = "127.0.0.1:0"
	}
	return &BusBridge{
		Kind:        kind,
		Bus:         bus,
		ListenAddr:  listen,
		ConnectAddr: connect,
		Timeout:     time.Second,
		Wait:        2 * time.Second,
		connCh:      make(chan struct{}, 1),
		reconnect:   make(chan struct{}, 1),
		done:        make(chan struct{}),
	}
}

// Start opens the listener or starts the dial loop.
func (b *BusBridge) Start() error {
	if b.ConnectAddr != "" {
		go b.dialLoop()
		return nil
	}
	ln, err := net.Listen("tcp", b.ListenAddr)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.ln = ln
	b.mu.Unlock()
	go b.acceptLoop(ln)
	b.logger().Info("bus bridge listening",
		slog.String("kind", string(b.Kind)),
		slog.Int("bus", b.Bus),
		slog.String("addr", ln.Addr().String()))
	return nil
}

func (b *BusBridge) logger() *slog.Logger { return loggerOr(b.Logger) }

func (b *BusBridge) timeout() time.Duration {
	if b.Timeout > 0 {
		return b.Timeout
	}
	return time.Second
}

func (b *BusBridge) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		b.setConn(conn)
	}
}

func (b *BusBridge) dialLoop() {
	for {
		conn, err := net.DialTimeout("tcp", b.ConnectAddr, b.timeout())
		if err != nil {
			b.logger().Debug("bus bridge dial",
				slog.String("kind", string(b.Kind)),
				slog.Int("bus", b.Bus),
				slog.String("remote", b.ConnectAddr),
				slog.Any("err", err))
			if b.sleep(250 * time.Millisecond) {
				return
			}
			continue
		}
		b.setConn(conn)
		select {
		case <-b.done:
			_ = conn.Close()
			return
		case <-b.reconnect:
		}
	}
}

func (b *BusBridge) sleep(d time.Duration) bool {
	select {
	case <-b.done:
		return true
	case <-time.After(d):
		return false
	}
}

func (b *BusBridge) setConn(conn net.Conn) {
	b.mu.Lock()
	old := b.conn
	b.conn = conn
	b.reader = bufio.NewReaderSize(conn, 4096)
	b.client = conn.RemoteAddr().String()
	b.mu.Unlock()

	if old != nil && old != conn {
		_ = old.Close()
	}
	b.logger().Info("bus bridge client",
		slog.String("kind", string(b.Kind)),
		slog.Int("bus", b.Bus),
		slog.String("remote", b.client))

	if b.Hello != "" {
		b.ioMu.Lock()
		_ = conn.SetWriteDeadline(time.Now().Add(b.timeout()))
		_, _ = conn.Write([]byte(b.Hello + "\n"))
		b.ioMu.Unlock()
	}
	select {
	case b.connCh <- struct{}{}:
	default:
	}
}

// firstWait consumes the one-time wait for a client. A client that is already
// connected makes it a no-op.
func (b *BusBridge) firstWait() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.waited || b.conn != nil {
		b.waited = true
		return 0
	}
	b.waited = true
	return b.Wait
}

// session returns the current connection, waiting at most wait for one.
func (b *BusBridge) session(wait time.Duration) (net.Conn, *bufio.Reader, error) {
	deadline := time.Now().Add(wait)
	for {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return nil, nil, errBusClosed
		}
		if b.conn != nil {
			conn, r := b.conn, b.reader
			b.mu.Unlock()
			return conn, r, nil
		}
		ch := b.connCh
		b.mu.Unlock()

		if wait <= 0 {
			return nil, nil, errBusNoClient
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, nil, errBusNoClient
		}
		select {
		case <-ch:
		case <-time.After(remaining):
			return nil, nil, errBusNoClient
		case <-b.done:
			return nil, nil, errBusClosed
		}
	}
}

func (b *BusBridge) writeLine(conn net.Conn, line string) error {
	_ = conn.SetWriteDeadline(time.Now().Add(b.timeout()))
	_, err := fmt.Fprintf(conn, "%s\n", line)
	return err
}

func (b *BusBridge) readLine(conn net.Conn, r *bufio.Reader) (string, error) {
	_ = conn.SetReadDeadline(time.Now().Add(b.timeout()))
	raw, err := r.ReadSlice('\n')
	if err != nil {
		return "", err
	}
	if len(raw) > 256 {
		return "", fmt.Errorf("bus bridge: response too long")
	}
	line := strings.TrimSpace(string(raw))
	if line == "" {
		return "", fmt.Errorf("bus bridge: empty response")
	}
	return line, nil
}

// request sends one line and waits for one response line.
func (b *BusBridge) request(line string) (string, error) {
	conn, r, err := b.session(b.firstWait())
	if err != nil {
		return "", err
	}
	b.ioMu.Lock()
	defer b.ioMu.Unlock()
	if err := b.writeLine(conn, line); err != nil {
		b.dropConn(conn)
		return "", err
	}
	resp, err := b.readLine(conn, r)
	if err != nil {
		b.dropConn(conn)
		return "", err
	}
	return resp, nil
}

// notify sends a line that has no response. waitFirst waits once for a client
// (used at transaction start so the device sees the whole transaction).
func (b *BusBridge) notify(line string, waitFirst bool) {
	wait := time.Duration(0)
	if waitFirst {
		wait = b.firstWait()
	}
	conn, _, err := b.session(wait)
	if err != nil {
		return
	}
	b.ioMu.Lock()
	defer b.ioMu.Unlock()
	if err := b.writeLine(conn, line); err != nil {
		b.dropConn(conn)
	}
}

func (b *BusBridge) dropConn(conn net.Conn) {
	b.mu.Lock()
	current := b.conn == conn
	if current {
		b.conn = nil
		b.reader = nil
		b.client = ""
	}
	b.mu.Unlock()
	_ = conn.Close()
	if current {
		b.logger().Info("bus bridge client gone",
			slog.String("kind", string(b.Kind)),
			slog.Int("bus", b.Bus))
		select {
		case b.reconnect <- struct{}{}:
		default:
		}
	}
}

// Status reports the listener, the dial target and the connected client.
func (b *BusBridge) Status() (listen, connect, client string, connected bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	listen = b.ListenAddr
	if b.ln != nil {
		listen = b.ln.Addr().String()
	}
	return listen, b.ConnectAddr, b.client, b.conn != nil
}

// Close stops the listener, disconnects the client and unblocks waiters.
func (b *BusBridge) Close() {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		ln := b.ln
		conn := b.conn
		b.ln = nil
		b.conn = nil
		b.reader = nil
		b.mu.Unlock()
		close(b.done)
		if ln != nil {
			_ = ln.Close()
		}
		if conn != nil {
			_ = conn.Close()
		}
	})
}

// responseToken returns the last whitespace-separated token, upper-cased.
func responseToken(resp string) string {
	f := strings.Fields(resp)
	if len(f) == 0 {
		return ""
	}
	return strings.ToUpper(f[len(f)-1])
}

func isAck(resp string) bool { return responseToken(resp) == "ACK" }
func isNak(resp string) bool { return responseToken(resp) == "NAK" }
