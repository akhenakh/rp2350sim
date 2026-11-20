package main

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// BusTerminal is the UI's manual device side of a Bus Pirate TCP bridge. It
// dials the bridge listener, shows the controller's request tokens, and sends
// the responses the user types. Responses may be queued before a request
// arrives; a multi-byte line such as "0xEF 0x40 0x18" is split into one
// response per request.
type BusTerminal struct {
	addr string
	conn net.Conn
	r    *bufio.Reader

	mu    sync.Mutex
	log   *TextRing
	queue chan string
	done  chan struct{}
	once  sync.Once
}

// NewBusTerminal connects to a bridge listener. The listener is already up;
// the short retry loop covers a just-created board.
func NewBusTerminal(addr string) (*BusTerminal, error) {
	deadline := time.Now().Add(2 * time.Second)
	var conn net.Conn
	var err error
	for {
		conn, err = net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t := &BusTerminal{
		addr:  addr,
		conn:  conn,
		r:     bufio.NewReader(conn),
		log:   NewTextRing(64 * 1024),
		queue: make(chan string, 256),
		done:  make(chan struct{}),
	}
	go t.loop()
	return t, nil
}

// Addr is the listener address this terminal is connected to.
func (t *BusTerminal) Addr() string { return t.addr }

func (t *BusTerminal) append(line string) {
	t.mu.Lock()
	t.log.AppendLine(line)
	t.mu.Unlock()
	RequestNextFrame()
}

func (t *BusTerminal) loop() {
	for {
		line, err := t.r.ReadString('\n')
		if err != nil {
			t.append("< disconnected")
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		t.append("< " + line)
		if !needsBusResponse(line) {
			continue
		}
		select {
		case resp := <-t.queue:
			t.append("> " + resp)
			if _, err := fmt.Fprintf(t.conn, "%s\n", resp); err != nil {
				t.append("< disconnected")
				return
			}
		case <-t.done:
			return
		}
	}
}

// needsBusResponse reports whether a bridge line expects a device answer.
// HELLO and the Bus Pirate stop token do not.
func needsBusResponse(line string) bool {
	if strings.Contains(line, "HELLO") || line == "]" {
		return false
	}
	return true
}

// Send queues one or more response tokens. Whitespace separates tokens, so a
// SPI command that expects three bytes can be answered with "0xEF 0x40 0x18".
// Queued tokens are logged immediately so a send is visible even before the
// firmware asks for the next byte.
func (t *BusTerminal) Send(line string) {
	tokens := strings.Fields(line)
	if len(tokens) == 0 {
		return
	}
	queued := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		select {
		case t.queue <- tok:
			queued = append(queued, tok)
		default:
			if len(queued) > 0 {
				t.append("+ queued " + strings.Join(queued, " "))
			}
			return
		}
	}
	if len(queued) > 0 {
		t.append("+ queued " + strings.Join(queued, " "))
	}
}

// Queued reports how many responses are waiting for a request.
func (t *BusTerminal) Queued() int { return len(t.queue) }

// Render draws the terminal log. It holds the log lock while LogView walks the
// ring, so the reader goroutine cannot append concurrently.
func (t *BusTerminal) Render() {
	t.mu.Lock()
	defer t.mu.Unlock()
	LogView(t.log, TextStyle(Fonts(Monospace...), FontSize(12), TextColor(140, 12, 85, 1)))
}

// Close disconnects the terminal.
func (t *BusTerminal) Close() {
	t.once.Do(func() {
		close(t.done)
		_ = t.conn.Close()
	})
}
