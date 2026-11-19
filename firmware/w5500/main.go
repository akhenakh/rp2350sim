// w5500 connects to a TCP echo server and sends a UDP datagram through the
// stock tinygo.org/x/drivers/w5500 driver, then reports the link state. It
// targets the breadboard wiring used by the `pico2-w5500` UI board: SPI0 with
// SCK GP18, MOSI GP19, MISO GP16 and CS GP17.
//
// The emulator backs the W5500 sockets with the host's network stack, so the
// loopback address below reaches a server on the machine running the
// emulator (see tools/ethhost).
package main

import (
	"fmt"
	"machine"
	"net"
	"net/netip"
	"time"

	"tinygo.org/x/drivers/netdev"
	"tinygo.org/x/drivers/w5500"
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
		Frequency: 30 * machine.MHz,
	})
	machine.GPIO17.Configure(machine.PinConfig{Mode: machine.PinOutput})
	machine.GPIO17.Set(true)

	eth := w5500.New(machine.SPI0, machine.GPIO17)
	if err := eth.Configure(w5500.Config{
		MAC:        net.HardwareAddr{0x02, 0x11, 0x22, 0x33, 0x44, 0x55},
		IP:         netip.AddrFrom4([4]byte{10, 0, 2, 15}),
		SubnetMask: netip.AddrFrom4([4]byte{255, 255, 255, 0}),
		Gateway:    netip.AddrFrom4([4]byte{10, 0, 2, 2}),
	}); err != nil {
		fmt.Printf("w5500 configure: %s\n", err)
		return
	}

	// Bind TinyGo's net package to the W5500.
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
	// A 4-byte net.IP is required: the stock driver rejects the IPv4-mapped
	// 16-byte form net.IPv4 returns.
	raddr := &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: tcpPort}
	conn, err := net.DialTCP("tcp", nil, raddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("hello w5500\n")); err != nil {
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

// tcpServer accepts one connection on tcpServe, echoes the request and
// reports the peer address. It accepts twice: the driver hands the
// established socket to the caller and re-arms the listener on a free
// hardware socket, so the second accept proves the re-arm path.
func tcpServer() {
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{Port: tcpServe})
	if err != nil {
		fmt.Printf("server: listen: %s\n", err)
		return
	}
	defer ln.Close()
	fmt.Printf("server: listening on :%d\n", tcpServe)

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

// udpSend uses the driver's socket API directly: TinyGo's net.DialUDP passes
// a zeroed address on its second netdev call (Bind then Connect), so the
// generic net path cannot be used for UDP in this build.
func udpSend(eth *w5500.Device) error {
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
	_, err = eth.Send(fd, []byte("udp w5500\n"), 0, time.Time{})
	return err
}
