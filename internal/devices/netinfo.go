package devices

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// NetworkInfo describes an attached network controller in terms a user can
// act on: the guest's configured IP parameters and the host endpoints that
// reach the emulated sockets.
type NetworkInfo struct {
	Kind       string       `json:"kind"`
	Configured bool         `json:"configured"`
	IP         string       `json:"ip,omitempty"`
	Mask       string       `json:"mask,omitempty"`
	Gateway    string       `json:"gateway,omitempty"`
	MAC        string       `json:"mac,omitempty"`
	Mode       string       `json:"mode,omitempty"`
	Sockets    []SocketInfo `json:"sockets,omitempty"`
}

// SocketInfo is one emulated hardware socket.
type SocketInfo struct {
	Index     int    `json:"index"`
	Proto     string `json:"proto"`
	State     string `json:"state"`
	LocalPort int    `json:"local_port,omitempty"`
	GuestAddr string `json:"guest_addr,omitempty"`
	// HostAddr is the address a program on this machine can connect to.
	HostAddr string `json:"host_addr,omitempty"`
	// Peer is the remote address for a connected or dialing socket.
	Peer string `json:"peer,omitempty"`
}

// Label is the protocol and port shown for a socket, e.g. "tcp/9002".
func (s SocketInfo) Label() string {
	if s.LocalPort == 0 {
		return s.Proto
	}
	return s.Proto + "/" + strconv.Itoa(s.LocalPort)
}

// Summary renders one line suitable for the runner's log.
func (n NetworkInfo) Summary() string {
	parts := []string{n.Kind}
	if n.Mode != "" {
		parts = append(parts, "mode="+n.Mode)
	}
	if n.IP != "" {
		parts = append(parts, "ip="+n.IP)
	}
	if n.Mask != "" {
		parts = append(parts, "mask="+n.Mask)
	}
	if n.Gateway != "" {
		parts = append(parts, "gw="+n.Gateway)
	}
	if n.MAC != "" {
		parts = append(parts, "mac="+n.MAC)
	}
	for _, s := range n.Sockets {
		seg := fmt.Sprintf("%s %s", s.Label(), s.State)
		if s.HostAddr != "" {
			seg += " host=" + s.HostAddr
		}
		if s.GuestAddr != "" {
			seg += " guest=" + s.GuestAddr
		}
		if s.Peer != "" {
			seg += " peer=" + s.Peer
		}
		parts = append(parts, seg)
	}
	return strings.Join(parts, " ")
}

func ipText(b []byte) string {
	if len(b) != 4 {
		return ""
	}
	if b[0]|b[1]|b[2]|b[3] == 0 {
		return ""
	}
	return net.IP(b).String()
}

func macText(b []byte) string {
	if len(b) != 6 || b[0]|b[1]|b[2]|b[3]|b[4]|b[5] == 0 {
		return ""
	}
	return net.HardwareAddr(b).String()
}

// reachableAddr turns a wildcard listener address into one a local program can
// dial; a listener on :9002 is reachable at 127.0.0.1:9002.
func reachableAddr(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func socketProto(proto byte) string {
	if proto == 2 {
		return "udp"
	}
	return "tcp"
}

func portString(port uint16) string { return strconv.Itoa(int(port)) }

// NetworkInfo returns the W5500's configured identity and live sockets.
func (w *W5500) NetworkInfo() NetworkInfo {
	w.mu.Lock()
	defer w.mu.Unlock()
	info := NetworkInfo{
		Kind:    "W5500",
		IP:      ipText(w.common[0x0F:0x13]),
		Mask:    ipText(w.common[0x05:0x09]),
		Gateway: ipText(w.common[0x01:0x05]),
		MAC:     macText(w.common[0x09:0x0F]),
	}
	info.Configured = info.IP != ""
	for n, s := range w.socks {
		if s.status == w5StatusClosed && s.listener == nil && s.conn == nil {
			continue
		}
		si := SocketInfo{
			Index:     n,
			Proto:     socketProto(s.proto),
			State:     w5StateName(s.status),
			LocalPort: int(s.srcPort),
		}
		if s.listener != nil {
			si.HostAddr = reachableAddr(s.listener.Addr())
		}
		if info.IP != "" && s.srcPort != 0 {
			si.GuestAddr = net.JoinHostPort(info.IP, portString(s.srcPort))
		}
		switch {
		case s.conn != nil:
			si.Peer = s.conn.RemoteAddr().String()
		case s.status == w5StatusInit && s.destPort != 0:
			si.Peer = net.JoinHostPort(net.IP(s.destIP[:]).String(), portString(s.destPort))
		}
		info.Sockets = append(info.Sockets, si)
	}
	return info
}

func w5StateName(status byte) string {
	switch status {
	case w5StatusInit:
		return "init"
	case w5StatusListen:
		return "listen"
	case w5StatusEstablished:
		return "established"
	case w5StatusCloseWait:
		return "close-wait"
	case w5StatusUDP:
		return "udp"
	}
	return "closed"
}

// NetworkInfo returns the W6300's configured identity and live sockets.
func (w *W6300) NetworkInfo() NetworkInfo {
	w.mu.Lock()
	defer w.mu.Unlock()
	info := NetworkInfo{
		Kind:    "W6300",
		IP:      ipText(w.common[w6SIPR : w6SIPR+4]),
		Mask:    ipText(w.common[w6SUBR : w6SUBR+4]),
		Gateway: ipText(w.common[w6GAR : w6GAR+4]),
		MAC:     macText(w.common[w6SHAR : w6SHAR+6]),
	}
	info.Configured = info.IP != ""
	for n, s := range w.socks {
		if s.status == w6StatusClosed && s.listener == nil && s.conn == nil {
			continue
		}
		si := SocketInfo{
			Index:     n,
			Proto:     socketProto(s.proto),
			State:     w6StateName(s.status),
			LocalPort: int(s.srcPort),
		}
		if s.listener != nil {
			si.HostAddr = reachableAddr(s.listener.Addr())
		}
		if info.IP != "" && s.srcPort != 0 {
			si.GuestAddr = net.JoinHostPort(info.IP, portString(s.srcPort))
		}
		switch {
		case s.conn != nil:
			si.Peer = s.conn.RemoteAddr().String()
		case s.status == w6StatusInit && s.destPort != 0:
			si.Peer = net.JoinHostPort(net.IP(s.destIP[:]).String(), portString(s.destPort))
		}
		info.Sockets = append(info.Sockets, si)
	}
	return info
}

func w6StateName(status byte) string {
	switch status {
	case w6StatusInit:
		return "init"
	case w6StatusListen:
		return "listen"
	case w6StatusEstablished:
		return "established"
	case w6StatusCloseWait:
		return "close-wait"
	case w6StatusUDP:
		return "udp"
	}
	return "closed"
}

// NetworkInfo returns the CH9120 module state. The caller can relabel a
// virtual Wi-Fi transport.
func (d *CH9120) NetworkInfo() NetworkInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	modes := []string{"tcp-server", "tcp-client", "udp-server", "udp-client"}
	info := NetworkInfo{
		Kind:       "CH9120",
		Configured: d.configured,
		Mode:       modes[d.mode&3],
		IP:         ipText(d.ip[:]),
		Mask:       ipText(d.mask[:]),
		Gateway:    ipText(d.gw[:]),
	}
	si := SocketInfo{LocalPort: int(d.localPort)}
	switch d.mode {
	case CH9120TCPServer, CH9120UDPServer:
		si.Proto = "tcp"
		if d.mode == CH9120UDPServer {
			si.Proto = "udp"
		}
		si.State = "listen"
		if d.listener != nil {
			si.HostAddr = reachableAddr(d.listener.Addr())
		} else if d.udp != nil {
			si.HostAddr = reachableAddr(d.udp.LocalAddr())
		}
		if d.conn != nil {
			si.State = "established"
			si.Peer = d.conn.RemoteAddr().String()
		}
	case CH9120TCPClient, CH9120UDPClient:
		si.Proto = "tcp"
		if d.mode == CH9120UDPClient {
			si.Proto = "udp"
		}
		si.State = "down"
		if d.connected {
			si.State = "established"
		}
		if d.conn != nil {
			si.Peer = d.conn.RemoteAddr().String()
		} else if d.udp != nil {
			si.Peer = d.udp.RemoteAddr().String()
		} else {
			si.Peer = net.JoinHostPort(net.IP(d.targetIP[:]).String(), portString(d.targetPort))
		}
	}
	if info.IP != "" && d.localPort != 0 {
		si.GuestAddr = net.JoinHostPort(info.IP, portString(d.localPort))
	}
	info.Sockets = append(info.Sockets, si)
	return info
}
