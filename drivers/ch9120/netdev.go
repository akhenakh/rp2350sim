package ch9120

import (
	"errors"
	"net/netip"
	"time"

	"tinygo.org/x/drivers/netdev"
)

// NetDevice adapts a CH9120 module to TinyGo's netdev interface. The module
// provides a single transparent TCP/UDP socket, so this is a one-socket
// netdev: Socket returns fd 0 and only one connection can be active. It lets
// firmware use net.DialTCP/ListenTCP while the "radio" is a host-backed serial
// link.
type NetDevice struct {
	dev  *Device
	base Config

	proto byte
	port  uint16
}

// NewNetDevice wraps a configured-capable module. base supplies the local IP,
// baud and Reconfigure callback; mode/target/port are replaced per operation.
func NewNetDevice(dev *Device, base Config) *NetDevice {
	return &NetDevice{dev: dev, base: base}
}

func (n *NetDevice) GetHostByName(name string) (netip.Addr, error) {
	return netip.Addr{}, errors.New("ch9120: DNS is not supported")
}

// Addr returns the configured local IP.
func (n *NetDevice) Addr() (netip.Addr, error) {
	return netip.AddrFrom4(n.base.LocalIP), nil
}

// Socket validates the request and remembers the protocol.
func (n *NetDevice) Socket(domain, stype, protocol int) (int, error) {
	if domain != netdev.AF_INET {
		return -1, netdev.ErrFamilyNotSupported
	}
	switch {
	case stype == netdev.SOCK_STREAM && protocol == netdev.IPPROTO_TCP:
		n.proto = 1
	case stype == netdev.SOCK_DGRAM && protocol == netdev.IPPROTO_UDP:
		n.proto = 2
	default:
		return -1, errors.New("ch9120: unsupported socket type")
	}
	return 0, nil
}

// Bind records the local port; the module is configured on Connect/Listen.
func (n *NetDevice) Bind(_ int, ip netip.AddrPort) error {
	n.port = ip.Port()
	return nil
}

// Connect switches the module to client mode, points it at the destination and
// waits for TCPCS (TCP only).
func (n *NetDevice) Connect(_ int, host string, ip netip.AddrPort) error {
	if host != "" {
		return errors.New("ch9120: DNS is not supported")
	}
	dst := ip.Addr().Unmap()
	if !dst.Is4() {
		return errors.New("ch9120: IPv4 destination required")
	}
	mode := byte(TCPClient)
	if n.proto == 2 {
		mode = UDPClient
	}
	cfg := n.base
	cfg.Mode = mode
	cfg.TargetIP = dst.As4()
	cfg.TargetPort = ip.Port()
	cfg.LocalPort = n.port
	if err := n.dev.Configure(cfg); err != nil {
		return err
	}
	if mode == TCPClient {
		return n.waitConnected(time.Now().Add(5 * time.Second))
	}
	return nil
}

// Listen switches the module to server mode on the bound port.
func (n *NetDevice) Listen(_ int, _ int) error {
	mode := byte(TCPServer)
	if n.proto == 2 {
		mode = UDPServer
	}
	cfg := n.base
	cfg.Mode = mode
	cfg.LocalPort = n.port
	return n.dev.Configure(cfg)
}

// Accept waits for an incoming client (the module reports connection over
// TCPCS). The peer address is not exposed by the CH9120 command set.
func (n *NetDevice) Accept(_ int) (int, netip.AddrPort, error) {
	if err := n.waitConnected(time.Now().Add(30 * time.Second)); err != nil {
		return -1, netip.AddrPort{}, err
	}
	return 0, netip.AddrPort{}, nil
}

// Send writes raw socket bytes.
func (n *NetDevice) Send(_ int, buf []byte, _ int, _ time.Time) (int, error) {
	return n.dev.Write(buf)
}

// Recv polls the UART for socket bytes until the buffer has data or the
// deadline expires.
func (n *NetDevice) Recv(_ int, buf []byte, _ int, deadline time.Time) (int, error) {
	for {
		if nlen, err := n.dev.Read(buf); nlen > 0 {
			return nlen, nil
		} else if err != nil {
			return 0, err
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return 0, netdev.ErrTimeout
		}
		time.Sleep(time.Millisecond)
	}
}

// Close has no equivalent in the CH9120 command set; the module keeps its
// socket until it is reconfigured or reset.
func (n *NetDevice) Close(_ int) error { return nil }

// SetSockOpt is not supported.
func (n *NetDevice) SetSockOpt(int, int, int, any) error { return netdev.ErrNotSupported }

func (n *NetDevice) waitConnected(deadline time.Time) error {
	for !n.dev.Connected() {
		if time.Now().After(deadline) {
			return netdev.ErrTimeout
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}
