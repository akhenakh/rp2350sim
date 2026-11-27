// pico2wnet exercises the CYW43439 data path: it joins the virtual AP, gets an
// address over DHCP, resolves the gateway with ARP and echoes a line through a
// host TCP server reached at 10.0.2.2.
package main

import (
	"context"
	"net/netip"
	"time"

	"github.com/soypat/cyw43439"
	"github.com/soypat/cyw43439/examples/cywnet"
	"github.com/soypat/lneto/dns"
	"github.com/soypat/lneto/ipv4"
	"github.com/soypat/lneto/tcp"
)

const (
	ssid      = "rp2350sim"
	pass      = "password"
	targetStr = "10.0.2.2:9000"
)

func main() {
	time.Sleep(500 * time.Millisecond)
	println("pico2wnet: init")
	cystack, err := cywnet.NewConfiguredPicoWithStack(ssid, pass, cyw43439.DefaultWifiConfig(), cywnet.StackConfig{
		Hostname:          "pico2w",
		MaxActiveTCPPorts: 1,
	})
	if err != nil {
		println("pico2wnet: setup failed:", err.Error())
		hang()
	}
	go loop(cystack)

	dhcp, err := cystack.SetupWithDHCP(cywnet.DHCPConfig{})
	if err != nil {
		println("pico2wnet: dhcp failed:", err.Error())
		hang()
	}
	println("pico2wnet: dhcp addr", ipv4.String(dhcp.AssignedAddr4), "router", dhcp.Router.String())

	stack := cystack.StackAsync()
	rstack := stack.StackRetrying(cywnet.DefaultStackBackoff)
	var addrs [1]netip.Addr
	if n, err := rstack.DoLookupIP(addrs[:], dns.MustNewName("app.lan"), 3*time.Second, 3); err != nil || n == 0 {
		println("pico2wnet: dns lookup failed")
	} else {
		println("pico2wnet: dns app.lan", addrs[0].String())
	}
	target, err := netip.ParseAddrPort(targetStr)
	if err != nil {
		println("pico2wnet: target:", err.Error())
		hang()
	}
	var conn tcp.Conn
	if err := conn.Configure(tcp.ConnConfig{
		RxBuf:             make([]byte, 1536),
		TxBuf:             make([]byte, 1536),
		TxPacketQueueSize: 3,
		RWBackoff:         cywnet.DefaultStackBackoff,
	}); err != nil {
		println("pico2wnet: conn:", err.Error())
		hang()
	}
	for {
		lport := uint16(stack.Prand32()>>17) + 1024
		if err := rstack.DoDialTCP(context.Background(), &conn, lport, target, 6*time.Second, 3); err != nil {
			println("pico2wnet: tcp dial:", err.Error())
			conn.Abort()
			time.Sleep(time.Second)
			continue
		}
		println("pico2wnet: tcp connected")
		if _, err := conn.Write([]byte("hello from pico2w\n")); err != nil {
			println("pico2wnet: tcp write:", err.Error())
		}
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 512)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				println("pico2wnet: echo:", string(buf[:n]))
				break
			}
			if err != nil {
				println("pico2wnet: tcp read:", err.Error())
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		conn.Close()
		time.Sleep(time.Second)
	}
}

func loop(stack *cywnet.Stack) {
	for {
		send, recv, _ := stack.RecvAndSend()
		if send == 0 && recv == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func hang() {
	for {
		time.Sleep(time.Second)
	}
}
