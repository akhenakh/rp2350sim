// wifi runs a TCP client through the virtual Wi-Fi netdev. The "radio" is a
// host-backed serial netdev (CH9120 emulation); the CYW43439 itself is not
// emulated. netdev.UseNetdev binds TinyGo's net package to the driver.
package main

import (
	"machine"
	"net"
	"time"

	"github.com/akhenakh/rp2350sim/drivers/ch9120"
	"tinygo.org/x/drivers/netdev"
)

func main() {
	cfg0 := machine.GPIO18
	rst := machine.GPIO19
	tcps := machine.GPIO17
	cfg0.Configure(machine.PinConfig{Mode: machine.PinOutput})
	rst.Configure(machine.PinConfig{Mode: machine.PinOutput})
	tcps.Configure(machine.PinConfig{Mode: machine.PinInput})

	uart := machine.UART1
	reconfigure := func(baud uint32) {
		uart.Configure(machine.UARTConfig{
			BaudRate: baud,
			TX:       machine.GPIO21,
			RX:       machine.GPIO20,
		})
	}
	reconfigure(9600)
	dev := ch9120.New(uart, cfg0, rst, tcps)
	nd := ch9120.NewNetDevice(dev, ch9120.Config{
		LocalIP:     [4]byte{10, 0, 0, 5},
		SubnetMask:  [4]byte{255, 255, 255, 0},
		Gateway:     [4]byte{10, 0, 0, 1},
		Baud:        115200,
		Reconfigure: reconfigure,
	})
	netdev.UseNetdev(nd)

	raddr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000}
	conn, err := net.DialTCP("tcp", nil, raddr)
	if err != nil {
		println("wifi: dial:", err.Error())
		return
	}
	defer conn.Close()
	println("wifi: connected")

	buf := make([]byte, 64)
	for {
		if _, err := conn.Write([]byte("hello wifi\n")); err != nil {
			println("wifi: write:", err.Error())
			time.Sleep(time.Second)
			continue
		}
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			println("wifi: read:", err.Error())
			time.Sleep(time.Second)
			continue
		}
		println("wifi echo:", string(buf[:n]))
		time.Sleep(500 * time.Millisecond)
	}
}
