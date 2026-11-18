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
	if err := udpSend(eth); err != nil {
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

	if _, err := conn.Write([]byte("hello w6300\n")); err != nil {
		return err
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		return err
	}
	fmt.Printf("tcp echo: %q\n", buf[:n])
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
	// socket after handing the established socket to the caller.
	for i := 0; i < 2; i++ {
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
			return
		}
		if _, err := conn.Write([]byte("pong\n")); err != nil {
			fmt.Printf("server: write: %s\n", err)
			conn.Close()
			return
		}
		fmt.Printf("server: got %q\n", buf[:n])
		conn.Close()
	}
}

func udpSend(eth *w6300.Device) error {
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
	_, err = eth.Send(fd, []byte("udp w6300\n"), 0, time.Time{})
	return err
}
