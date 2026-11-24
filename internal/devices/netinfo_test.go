package devices

import (
	"net"
	"strings"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

func TestReachableAddr(t *testing.T) {
	cases := map[string]string{
		"[::]:9002":    "127.0.0.1:9002",
		"0.0.0.0:9002": "127.0.0.1:9002",
		":9002":        "127.0.0.1:9002",
		"10.0.0.2:80":  "10.0.0.2:80",
		"127.0.0.1:1":  "127.0.0.1:1",
	}
	for in, want := range cases {
		if got := reachableAddr(fakeAddr(in)); got != want {
			t.Errorf("reachableAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestW5500NetworkInfo(t *testing.T) {
	w := NewW5500(rp2350.New(), 17)
	copy(w.common[0x0F:0x13], []byte{10, 0, 2, 15})
	copy(w.common[0x05:0x09], []byte{255, 255, 255, 0})
	copy(w.common[0x01:0x05], []byte{10, 0, 2, 2})
	copy(w.common[0x09:0x0F], []byte{2, 0x11, 0x22, 0x33, 0x44, 0x55})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	w.socks[0].proto = 1
	w.socks[0].srcPort = 9002
	w.socks[0].status = w5StatusListen
	w.socks[0].listener = ln

	info := w.NetworkInfo()
	if !info.Configured || info.IP != "10.0.2.15" || info.Gateway != "10.0.2.2" {
		t.Fatalf("identity = %+v", info)
	}
	if info.MAC != "02:11:22:33:44:55" {
		t.Fatalf("mac = %q", info.MAC)
	}
	if len(info.Sockets) != 1 {
		t.Fatalf("sockets = %+v", info.Sockets)
	}
	s := info.Sockets[0]
	if s.State != "listen" || s.LocalPort != 9002 || s.GuestAddr != "10.0.2.15:9002" {
		t.Fatalf("socket = %+v", s)
	}
	if s.HostAddr != ln.Addr().String() {
		t.Fatalf("host = %q, want %q", s.HostAddr, ln.Addr().String())
	}
	if !strings.Contains(info.Summary(), "tcp/9002 listen") {
		t.Fatalf("summary = %q", info.Summary())
	}
}

func TestW6300NetworkInfo(t *testing.T) {
	w := NewW6300(rp2350.New(), 17)
	copy(w.common[w6SIPR:w6SIPR+4], []byte{192, 168, 7, 9})
	copy(w.common[w6SUBR:w6SUBR+4], []byte{255, 255, 255, 0})
	copy(w.common[w6GAR:w6GAR+4], []byte{192, 168, 7, 1})
	copy(w.common[w6SHAR:w6SHAR+6], []byte{2, 0xaa, 0xbb, 0xcc, 0xdd, 0xee})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	w.socks[2].proto = 1
	w.socks[2].srcPort = 8080
	w.socks[2].status = w6StatusListen
	w.socks[2].listener = ln

	info := w.NetworkInfo()
	if info.IP != "192.168.7.9" || info.MAC != "02:aa:bb:cc:dd:ee" {
		t.Fatalf("identity = %+v", info)
	}
	if len(info.Sockets) != 1 || info.Sockets[0].Index != 2 || info.Sockets[0].State != "listen" {
		t.Fatalf("sockets = %+v", info.Sockets)
	}
	if info.Sockets[0].HostAddr != ln.Addr().String() {
		t.Fatalf("host = %q", info.Sockets[0].HostAddr)
	}
}

func TestUDPServerNetworkInfoHostAddr(t *testing.T) {
	for _, kind := range []string{"w5500", "w6300"} {
		t.Run(kind, func(t *testing.T) {
			conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			var info NetworkInfo
			if kind == "w5500" {
				w := NewW5500(rp2350.New(), 17)
				w.socks[0].proto = 2
				w.socks[0].status = w5StatusUDP
				w.socks[0].srcPort = uint16(conn.LocalAddr().(*net.UDPAddr).Port)
				w.socks[0].udp = conn
				w.socks[0].udpServer = true
				info = w.NetworkInfo()
			} else {
				w := NewW6300(rp2350.New(), 17)
				w.socks[0].proto = 2
				w.socks[0].status = w6StatusUDP
				w.socks[0].srcPort = uint16(conn.LocalAddr().(*net.UDPAddr).Port)
				w.socks[0].udp = conn
				w.socks[0].udpServer = true
				info = w.NetworkInfo()
			}
			if len(info.Sockets) != 1 || info.Sockets[0].HostAddr != reachableAddr(conn.LocalAddr()) {
				t.Fatalf("UDP server info = %+v", info.Sockets)
			}
		})
	}
}

func TestCH9120NetworkInfo(t *testing.T) {
	d := NewCH9120(rp2350.New(), 1, 18, 19, 17)
	// Defaults are not a firmware-created network.
	if info := d.NetworkInfo(); info.Configured {
		t.Fatalf("defaults reported as configured: %+v", info)
	}
	d.ip = [4]byte{192, 168, 1, 200}
	d.mask = [4]byte{255, 255, 255, 0}
	d.gw = [4]byte{192, 168, 1, 1}
	d.localPort = 8080
	d.mode = CH9120TCPServer
	d.configured = true
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	d.listener = ln
	// The listener's actual port is ephemeral; report the configured port in
	// the info and the real listener in the host address.
	info := d.NetworkInfo()
	if !info.Configured || info.Mode != "tcp-server" || info.IP != "192.168.1.200" {
		t.Fatalf("info = %+v", info)
	}
	if len(info.Sockets) != 1 || info.Sockets[0].State != "listen" {
		t.Fatalf("sockets = %+v", info.Sockets)
	}
	if info.Sockets[0].HostAddr != ln.Addr().String() {
		t.Fatalf("host = %q", info.Sockets[0].HostAddr)
	}
}
