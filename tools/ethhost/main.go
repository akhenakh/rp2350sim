// ethhost is a tiny TCP echo and UDP sink server used to exercise the W5500
// and W6300 models. It listens on 127.0.0.1 so emulated firmware can reach it
// through the host-backed sockets. It runs until killed.
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	tcpPort := flag.Int("tcp", 9000, "TCP echo port")
	udpPort := flag.Int("udp", 9001, "UDP sink port")
	checkPort := flag.Int("check", 9002, "connect to emulated server on this port after 1s")
	udpReply := flag.Bool("udp-reply", false, "answer each datagram with two queued datagrams")
	flag.Parse()

	tcpLn, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *tcpPort))
	if err != nil {
		fmt.Fprintln(os.Stderr, "ethhost:", err)
		os.Exit(1)
	}
	defer func() { _ = tcpLn.Close() }()

	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", *udpPort))
	if err != nil {
		fmt.Fprintln(os.Stderr, "ethhost:", err)
		os.Exit(1)
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ethhost:", err)
		os.Exit(1)
	}
	defer func() { _ = udpConn.Close() }()

	fmt.Printf("ethhost: tcp %s, udp %s\n", tcpLn.Addr(), udpConn.LocalAddr())

	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := udpConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			fmt.Printf("udp from %s: %q\n", from, buf[:n])
			if *udpReply {
				for _, msg := range []string{"first\n", "second\n"} {
					if _, err := udpConn.WriteToUDP([]byte(msg), from); err != nil {
						fmt.Printf("udp reply to %s: %v\n", from, err)
						return
					}
				}
			}
		}
	}()

	go func() {
		for {
			conn, err := tcpLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				n, err := io.Copy(conn, conn)
				if err != nil {
					fmt.Printf("tcp %s: %v\n", conn.RemoteAddr(), err)
					return
				}
				fmt.Printf("tcp %s: echoed %d bytes\n", conn.RemoteAddr(), n)
			}(conn)
		}
	}()

	go func() {
		// Give the emulated firmware time to listen, then connect twice so
		// the second accept proves the listener re-armed.
		time.Sleep(time.Second)
		for i := range 2 {
			// The emulated listener re-arms asynchronously; retry while the
			// firmware catches up with wall time.
			addr := fmt.Sprintf("127.0.0.1:%d", *checkPort)
			var conn net.Conn
			var err error
			deadline := time.Now().Add(5 * time.Second)
			for {
				conn, err = net.Dial("tcp", addr)
				if err == nil || time.Now().After(deadline) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if err != nil {
				fmt.Printf("server check %d: dial: %v\n", i+1, err)
				return
			}
			if _, err := conn.Write([]byte("ping\n")); err != nil {
				fmt.Printf("server check %d: write: %v\n", i+1, err)
				_ = conn.Close()
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			buf := make([]byte, 64)
			n, err := conn.Read(buf)
			_ = conn.Close()
			if err != nil {
				fmt.Printf("server check %d: read: %v\n", i+1, err)
				return
			}
			fmt.Printf("server check %d: reply %q\n", i+1, buf[:n])
			time.Sleep(300 * time.Millisecond)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}
