// Package ch9120 controls a WCH CH9120 UART-to-Ethernet module over its serial
// configuration command set. The module handles the TCP/IP stack itself; the
// MCU configures a mode and then exchanges raw bytes with the socket.
package ch9120

import (
	"errors"
	"time"
)

// Modes match the CH9120 mode command parameter.
const (
	TCPServer = 0
	TCPClient = 1
	UDPServer = 2
	UDPClient = 3
)

// UART is the subset of machine.UART the driver needs.
type UART interface {
	Read(p []byte) (int, error)
	ReadByte() (byte, error)
	Write(p []byte) (int, error)
}

// Pin is an output pin.
type Pin interface {
	Set(high bool)
}

// InputPin is a readable pin (TCPCS is active low).
type InputPin interface {
	Get() bool
}

// Config is the module configuration. Addresses are in network order.
type Config struct {
	Mode       byte
	LocalIP    [4]byte
	SubnetMask [4]byte
	Gateway    [4]byte
	TargetIP   [4]byte
	LocalPort  uint16
	TargetPort uint16
	Baud       uint32

	// CommandBaud is the configuration-mode rate; the datasheet fixes it at
	// 9600 baud. Zero uses 9600.
	CommandBaud uint32
	// Reconfigure changes the UART rate (and pins) between command and data
	// mode. Optional when the UART is fixed at the data rate.
	Reconfigure func(baud uint32)
}

// Device is a configured CH9120.
type Device struct {
	uart UART
	cfg0 Pin
	rst  Pin
	tcps InputPin

	reconfigure func(baud uint32)
	dataBaud    uint32
	commandBaud uint32
	timeout     time.Duration
}

// New creates a driver. cfg0 is the configuration-mode pin (low enters
// configuration), rst the active-low reset, tcps the TCP-connected indicator.
func New(uart UART, cfg0, rst Pin, tcps InputPin) *Device {
	return &Device{
		uart:        uart,
		cfg0:        cfg0,
		rst:         rst,
		tcps:        tcps,
		commandBaud: 9600,
		timeout:     500 * time.Millisecond,
	}
}

// Configure enters configuration mode, writes the parameters, applies them and
// switches the UART to the data baud.
func (d *Device) Configure(cfg Config) error {
	if cfg.Baud == 0 {
		cfg.Baud = 115200
	}
	d.dataBaud = cfg.Baud
	if cfg.CommandBaud != 0 {
		d.commandBaud = cfg.CommandBaud
	}
	d.reconfigure = cfg.Reconfigure

	if d.rst != nil {
		d.rst.Set(false) // reset pulse
		time.Sleep(10 * time.Millisecond)
		d.rst.Set(true)
		time.Sleep(10 * time.Millisecond)
	}
	if d.cfg0 != nil {
		d.cfg0.Set(false)
	}
	d.setBaud(d.commandBaud)
	time.Sleep(20 * time.Millisecond)

	cmds := []struct {
		code byte
		args []byte
	}{
		{0x10, []byte{cfg.Mode & 3}},
		{0x11, cfg.LocalIP[:]},
		{0x12, cfg.SubnetMask[:]},
		{0x13, cfg.Gateway[:]},
		{0x14, []byte{byte(cfg.LocalPort >> 8), byte(cfg.LocalPort)}},
		{0x15, cfg.TargetIP[:]},
		{0x16, []byte{byte(cfg.TargetPort >> 8), byte(cfg.TargetPort)}},
		{0x21, []byte{byte(cfg.Baud), byte(cfg.Baud >> 8), byte(cfg.Baud >> 16), byte(cfg.Baud >> 24)}},
		{0x22, []byte{0x01, 0x04, 0x08}}, // 1 stop, no parity, 8 data
		{0x0d, nil},                      // save
		{0x0e, nil},                      // apply and reset
	}
	for _, cmd := range cmds {
		if err := d.command(cmd.code, cmd.args...); err != nil {
			return err
		}
	}

	if d.cfg0 != nil {
		d.cfg0.Set(true)
	}
	d.setBaud(d.dataBaud)
	time.Sleep(50 * time.Millisecond)
	return nil
}

// Version queries the two-byte chip version.
func (d *Device) Version() (uint16, error) {
	if d.rst != nil {
		d.rst.Set(false)
		time.Sleep(5 * time.Millisecond)
		d.rst.Set(true)
		time.Sleep(5 * time.Millisecond)
	}
	if d.cfg0 != nil {
		d.cfg0.Set(false)
	}
	d.setBaud(d.commandBaud)
	time.Sleep(10 * time.Millisecond)
	if err := d.write([]byte{0x57, 0xab, 0x01}); err != nil {
		return 0, err
	}
	a, err := d.readByte()
	if err != nil {
		return 0, err
	}
	b, err := d.readByte()
	if err != nil {
		return 0, err
	}
	if d.cfg0 != nil {
		d.cfg0.Set(true)
	}
	d.setBaud(d.dataBaud)
	return uint16(a)<<8 | uint16(b), nil
}

// Read reads socket bytes from the module.
func (d *Device) Read(p []byte) (int, error) { return d.uart.Read(p) }

// Write sends bytes to the socket.
func (d *Device) Write(p []byte) (int, error) { return d.uart.Write(p) }

// Connected reports the TCPCS indicator (low means connected).
func (d *Device) Connected() bool {
	if d.tcps == nil {
		return false
	}
	return !d.tcps.Get()
}

func (d *Device) setBaud(baud uint32) {
	if d.reconfigure != nil {
		d.reconfigure(baud)
	}
}

func (d *Device) command(code byte, args ...byte) error {
	frame := make([]byte, 0, 3+len(args))
	frame = append(frame, 0x57, 0xab, code)
	frame = append(frame, args...)
	if err := d.write(frame); err != nil {
		return err
	}
	got, err := d.readByte()
	if err != nil {
		return err
	}
	if got != 0xaa {
		return errors.New("ch9120: command 0x" + hex(code) + " returned 0x" + hex(got))
	}
	return nil
}

func (d *Device) write(p []byte) error {
	_, err := d.uart.Write(p)
	return err
}

func (d *Device) readByte() (byte, error) {
	deadline := time.Now().Add(d.timeout)
	for {
		b, err := d.uart.ReadByte()
		if err == nil {
			return b, nil
		}
		if time.Now().After(deadline) {
			return 0, errors.New("ch9120: response timeout")
		}
		time.Sleep(time.Millisecond)
	}
}

func hex(b byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[b>>4], digits[b&0xf]})
}
