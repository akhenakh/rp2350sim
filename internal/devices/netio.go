package devices

import (
	"io"
	"net"
	"time"
)

const defaultNetworkWriteTimeout = 2 * time.Second

func networkWriteTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	return defaultNetworkWriteTimeout
}

func writeNet(conn net.Conn, data []byte, timeout time.Duration) (int, error) {
	if err := conn.SetWriteDeadline(time.Now().Add(networkWriteTimeout(timeout))); err != nil {
		return 0, err
	}
	n, err := conn.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return n, err
}

func writeUDPNet(conn *net.UDPConn, data []byte, addr *net.UDPAddr, timeout time.Duration) (int, error) {
	if err := conn.SetWriteDeadline(time.Now().Add(networkWriteTimeout(timeout))); err != nil {
		return 0, err
	}
	n, err := conn.WriteToUDP(data, addr)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return n, err
}
