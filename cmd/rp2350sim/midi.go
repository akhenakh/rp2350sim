package main

import (
	hexpkg "encoding/hex"
	"log/slog"
	"net"
	"sync"

	"github.com/akhenakh/rp2350sim/internal/midi"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// midiBridge connects firmware MIDI (USB-MIDI and/or a UART) to a UDP-MIDI
// endpoint and the CLI monitor. Host-to-device messages are queued and sent
// from the emulator thread so the USB DPRAM is never touched concurrently.
type midiBridge struct {
	chip    *rp2350.Chip
	usb     bool
	uart    int // -1 disables the UART tap
	monitor bool
	log     *slog.Logger

	mu      sync.Mutex
	parser  midi.Parser
	obs     midi.Parser
	pending []midi.Message

	udp  *net.UDPConn
	peer *net.UDPAddr
}

func newMIDIBridge(c *rp2350.Chip, usb bool, uart int, monitor bool, log *slog.Logger) *midiBridge {
	b := &midiBridge{chip: c, usb: usb, uart: uart, monitor: monitor, log: log}
	if usb {
		c.EnableUSBMIDI()
		c.SetUSBMIDI(func(data []byte) {
			for _, m := range midi.DecodeUSB(data) {
				b.emit(m)
			}
		})
	}
	if uart == 0 || uart == 1 {
		prev := c.OnUART0
		if uart == 1 {
			prev = c.OnUART1
		}
		feed := func(v byte) {
			if prev != nil {
				prev(v)
			}
			b.observe([]byte{v})
		}
		if uart == 0 {
			c.OnUART0 = feed
		} else {
			c.OnUART1 = feed
		}
	}
	return b
}

// listenUDP opens a bidirectional UDP-MIDI endpoint. Datagrams received from
// the host are queued for the firmware; firmware messages go back to the last
// sender.
func (b *midiBridge) listenUDP(addr string) error {
	uaddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", uaddr)
	if err != nil {
		return err
	}
	b.udp = conn
	b.log.Info("midi udp listening", slog.String("addr", conn.LocalAddr().String()))
	go func() {
		buf := make([]byte, 2048)
		for {
			n, peer, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			b.mu.Lock()
			b.peer = peer
			b.mu.Unlock()
			b.feed(append([]byte(nil), buf[:n]...))
		}
	}()
	return nil
}

// feed parses a raw MIDI byte stream from the host or a UART.
func (b *midiBridge) feed(data []byte) {
	b.mu.Lock()
	for _, v := range data {
		for _, m := range b.parser.Feed(v) {
			if len(b.pending) < 256 {
				b.pending = append(b.pending, m)
			}
		}
	}
	b.mu.Unlock()
}

// observe reports a firmware-to-host MIDI byte stream: the UART tap sees the
// firmware's TX bytes, which must never be re-injected (an echo firmware would
// loop forever). Only the UDP/UI feed queues host-to-device messages.
func (b *midiBridge) observe(data []byte) {
	b.mu.Lock()
	var msgs []midi.Message
	for _, v := range data {
		msgs = append(msgs, b.obs.Feed(v)...)
	}
	b.mu.Unlock()
	for _, m := range msgs {
		b.emit(m)
	}
}

func (b *midiBridge) queue(m midi.Message) {
	b.mu.Lock()
	if len(b.pending) < 256 {
		b.pending = append(b.pending, m)
	}
	b.mu.Unlock()
}

// tick flushes host-to-device messages on the emulator thread.
func (b *midiBridge) tick() {
	b.mu.Lock()
	pending := b.pending
	b.pending = nil
	b.mu.Unlock()
	for _, m := range pending {
		if b.usb {
			pkt := midi.USBMessage(m)
			if b.chip.SendUSBMIDI(pkt[:]) {
				continue
			}
			// Requeue if the firmware has not armed the endpoint yet.
			b.queue(m)
			continue
		}
		if b.uart == 0 || b.uart == 1 {
			b.chip.SendUART(b.uart, m.Bytes())
			b.log.Debug("midi to uart", slog.Int("uart", b.uart), slog.String("message", m.String()))
		}
	}
}

// emit reports a firmware-to-host message to the monitor and UDP peer.
func (b *midiBridge) emit(m midi.Message) {
	if b.monitor {
		b.log.Info("midi", slog.String("message", m.String()), slog.String("bytes", hexpkg.EncodeToString(m.Bytes())))
	}
	b.mu.Lock()
	peer := b.peer
	conn := b.udp
	b.mu.Unlock()
	if peer != nil && conn != nil {
		_, _ = conn.WriteToUDP(m.Bytes(), peer)
	}
}

func (b *midiBridge) close() {
	if b.udp != nil {
		_ = b.udp.Close()
	}
}
