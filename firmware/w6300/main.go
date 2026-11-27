// w6300 connects to a TCP echo server and sends a UDP datagram through the
// W6300 driver, then reports the link state. It targets the RP2350-POE-ETH
// wiring: SPI0 with SCK GP18, MOSI GP19, MISO GP16 and CS GP17.
//
// The emulator backs the W6300 sockets with the host's network stack, so the
// loopback address below reaches a server on the machine running the
// emulator (see tools/ethhost).
package main

import (
	"fmt"
	"io"
	"machine"
	"net"
	"net/netip"
	"time"

	"github.com/akhenakh/rp2350sim/drivers/w6300"
	"tinygo.org/x/drivers/netdev"
)

const (
	tcpPort  = 9000 // echo client
	udpPort  = 9001 // datagram sink
	tcpServe = 9002 // listening server
)

func main() {
	machine.SPI0.Configure(machine.SPIConfig{
		SCK:       machine.GPIO18,
		SDO:       machine.GPIO19,
		SDI:       machine.GPIO16,
		Frequency: 70 * machine.MHz,
	})
	machine.GPIO17.Configure(machine.PinConfig{Mode: machine.PinOutput})
	machine.GPIO20.Configure(machine.PinConfig{Mode: machine.PinOutput})
	machine.GPIO20.Set(true) // release the hardware reset

	eth := w6300.New(machine.SPI0, machine.GPIO17)
	if err := eth.Configure(w6300.Config{
		MAC:        net.HardwareAddr{0x02, 0x11, 0x22, 0x33, 0x44, 0x55},
		IP:         netip.AddrFrom4([4]byte{10, 0, 2, 15}),
		SubnetMask: netip.AddrFrom4([4]byte{255, 255, 255, 0}),
		Gateway:    netip.AddrFrom4([4]byte{10, 0, 2, 2}),
	}); err != nil {
		fmt.Printf("w6300 configure: %s\n", err)
		return
	}

	major, minor := eth.ChipID()
	ip, _ := eth.Addr()
	fmt.Printf("w6300 id=%04x:%02x ip=%s link=%s\n", major, minor, ip, eth.LinkInfo())

	// Bind TinyGo's net package to the W6300.
	netdev.UseNetdev(eth)

	go tcpServer()

	if err := tcpEcho(); err != nil {
		fmt.Printf("tcp: %s\n", err)
	} else {
		fmt.Println("tcp: ok")
	}
	if err := udpRoundTrip(eth); err != nil {
		fmt.Printf("udp: %s\n", err)
	} else {
		fmt.Println("udp: ok")
	}

	for {
		time.Sleep(time.Second)
	}
}

func tcpEcho() error {
	raddr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: tcpPort}
	conn, err := net.DialTCP("tcp", nil, raddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	payload := []byte("split-read-payload-0123456789\n")
	if _, err := conn.Write(payload); err != nil {
		return err
	}
	// No guest deadline: the emulator can run a long way past a short virtual
	// timeout before the host answers. A short read must leave the rest of the
	// stream queued in the W6300.
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return err
	}
	rest := make([]byte, len(payload)-len(hdr))
	if _, err := io.ReadFull(conn, rest); err != nil {
		return err
	}
	got := append(hdr, rest...)
	if string(got) != string(payload) {
		return fmt.Errorf("split read got %q, want %q", got, payload)
	}
	fmt.Printf("tcp echo: %q\n", got)
	fmt.Println("tcp split: ok")
	return nil
}

// udpSend uses the driver's socket API directly: TinyGo's net.DialUDP passes
// a zeroed address on its second netdev call (Bind then Connect), so the
// generic net path cannot be used for UDP in this build.
// tcpServer accepts one connection on tcpServe, echoes the request and
// reports the peer address.
func tcpServer() {
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{Port: tcpServe})
	if err != nil {
		fmt.Printf("server: listen: %s\n", err)
		return
	}
	defer ln.Close()
	fmt.Printf("server: listening on :%d\n", tcpServe)

	// Accept twice: the driver re-arms the listener on a free hardware
	// socket after handing the established socket to the caller. A peer can
	// close between the response and the next accept, so per-connection
	// errors continue instead of ending the server.
	served := 0
	for served < 2 {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Printf("server: accept: %s\n", err)
			return
		}
		fmt.Printf("server: accepted %s\n", conn.RemoteAddr())

		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err != nil {
			fmt.Printf("server: read: %s\n", err)
			conn.Close()
			continue
		}
		if _, err := conn.Write([]byte("pong\n")); err != nil {
			fmt.Printf("server: write: %s\n", err)
			conn.Close()
			continue
		}
		fmt.Printf("server: got %q\n", buf[:n])
		conn.Close()
		served++
	}
}

func udpRoundTrip(eth *w6300.Device) error {
	fd, err := eth.Socket(netdev.AF_INET, netdev.SOCK_DGRAM, netdev.IPPROTO_UDP)
	if err != nil {
		return err
	}
	defer eth.Close(fd)

	if err := eth.Bind(fd, netip.AddrPortFrom(netip.IPv4Unspecified(), 0)); err != nil {
		return err
	}
	server := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	if err := eth.Connect(fd, "", netip.AddrPortFrom(server, udpPort)); err != nil {
		return err
	}
	if _, err := eth.Send(fd, []byte("udp w6300\n"), 0, time.Time{}); err != nil {
		return err
	}
	// ethhost -udp-reply queues two datagrams before the guest reads. Each
	// Recv must return one datagram, not the concatenation.
	first := make([]byte, 16)
	n1, err := eth.Recv(fd, first, 0, time.Time{})
	if err != nil {
		return err
	}
	second := make([]byte, 16)
	n2, err := eth.Recv(fd, second, 0, time.Time{})
	if err != nil {
		return err
	}
	if string(first[:n1]) != "first\n" || string(second[:n2]) != "second\n" {
		return fmt.Errorf("datagrams %q %q", first[:n1], second[:n2])
	}
	fmt.Printf("udp datagrams: %q %q\n", first[:n1], second[:n2])
	return nil
}
