package w6300

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"runtime"
	"time"

	"tinygo.org/x/drivers/netdev"
)

type socket struct {
	sockn    uint8
	protocol uint8
	port     uint16
	inUse    bool
	closed   bool
}

func (s *socket) setProtocol(proto byte) *socket {
	s.protocol = proto
	return s
}

func (s *socket) setPort(port uint16) *socket {
	s.port = port
	return s
}

func (s *socket) setInUse(inUse bool) *socket {
	s.inUse = inUse
	return s
}

func (s *socket) setClosed(closed bool) *socket {
	s.closed = closed
	return s
}

func (s *socket) reset() {
	s.protocol = 0
	s.port = 0
	s.inUse = false
	s.closed = false
}

// GetHostByName resolves the given host name to an IP address.
func (d *Device) GetHostByName(name string) (netip.Addr, error) {
	d.mu.Lock()
	dns := d.dns
	d.mu.Unlock()

	if dns == nil {
		return netip.Addr{}, netdev.ErrNotSupported
	}
	return dns(name)
}

// Socket allocates a hardware socket and puts it in the requested mode. The
// socket is opened by Bind or, for unbound clients, by Connect.
func (d *Device) Socket(domain int, stype int, protocol int) (int, error) {
	if domain != netdev.AF_INET {
		return -1, netdev.ErrFamilyNotSupported
	}
	switch {
	case stype == netdev.SOCK_STREAM && protocol == netdev.IPPROTO_TCP:
	case stype == netdev.SOCK_DGRAM && protocol == netdev.IPPROTO_UDP:
	default:
		return -1, errors.New("w6300: unsupported combination of socket type and protocol")
	}

	var proto byte
	switch protocol {
	case netdev.IPPROTO_TCP:
		proto = 1
	case netdev.IPPROTO_UDP:
		proto = 2
	default:
		return -1, netdev.ErrNotSupported
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	sockfd, sock, err := d.nextSocket()
	if err != nil {
		return -1, err
	}

	d.openSocket(sock.sockn, proto)

	sock.setProtocol(proto).setInUse(true)
	return sockfd, nil
}

func (d *Device) openSocket(sockn uint8, proto byte) {
	d.writeByte(sockMode, sockAddr(sockn), proto&0x0F)
}

// Bind sets the local port and opens the socket.
func (d *Device) Bind(sockfd int, ip netip.AddrPort) error {
	// The IP address is irrelevant: the configured source address is used.
	port := ip.Port()

	d.mu.Lock()
	defer d.mu.Unlock()

	sock, err := d.socket(sockfd)
	if err != nil {
		return err
	}

	if err = d.bindSocket(sock.sockn, port); err != nil {
		return errors.New("w6300: could not set socket port: " + err.Error())
	}

	sock.setPort(port)
	return nil
}

func (d *Device) bindSocket(sockn uint8, port uint16) error {
	d.writeUint16(sockSrcPort, sockAddr(sockn), port)
	d.socketSendCmd(sockn, sockCmdOpen)
	if d.sockStatus(sockn) == sockStatusClosed {
		return errors.New("socket is closed after opening")
	}
	return nil
}

// SetSockOpt is not supported by the W6300.
func (d *Device) SetSockOpt(int, int, int, any) error {
	return netdev.ErrNotSupported
}

// Connect opens the socket if needed and establishes a connection to the
// destination, blocking until the controller reports CON or TIMEOUT.
func (d *Device) Connect(sockfd int, host string, ip netip.AddrPort) error {
	destIP := ip.Addr()
	if host != "" {
		var err error
		destIP, err = d.GetHostByName(host)
		if err != nil {
			return errors.New("w6300: could not resolve host " + host + ": " + err.Error())
		}
	}
	// net.IPv4 returns an IPv4-mapped 16-byte address; the chip only speaks
	// IPv4.
	destIP = destIP.Unmap()
	if !destIP.IsValid() || !destIP.Is4() {
		return errors.New("w6300: invalid destination IP address: " + destIP.String())
	}
	port := ip.Port()

	d.mu.Lock()
	defer d.mu.Unlock()

	sock, err := d.socket(sockfd)
	if err != nil {
		return err
	}

	// A client socket that was never bound still needs an OPEN command
	// before CONNECT is accepted.
	if d.sockStatus(sock.sockn) == sockStatusClosed {
		d.socketSendCmd(sock.sockn, sockCmdOpen)
	}

	d.write(sockDestIP, sockAddr(sock.sockn), destIP.AsSlice())
	d.writeUint16(sockDestPort, sockAddr(sock.sockn), port)
	d.socketSendCmd(sock.sockn, sockCmdConnect)

	irq := d.irqPoll(sock.sockn, sockIntConnect|sockIntDisconnect|sockIntTimeout, time.Time{})
	switch {
	case irq&sockIntTimeout != 0:
		return netdev.ErrTimeout
	case irq&sockIntDisconnect != 0:
		return net.ErrClosed
	case irq&sockIntConnect == 0:
		return netdev.ErrTimeout
	}
	return nil
}

// Listen puts a TCP socket into listening mode. The backlog is ignored.
func (d *Device) Listen(sockfd int, _ int) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	sock, err := d.socket(sockfd)
	if err != nil {
		return err
	}

	if sock.protocol != 1 {
		return errors.New("w6300: not a TCP socket")
	}

	if err = d.listen(sock.sockn); err != nil {
		return errors.New("w6300: could not send listen command: " + err.Error())
	}
	return nil
}

func (d *Device) listen(sockn uint8) error {
	if d.sockStatus(sockn) != sockStatusInit {
		return errors.New("socket is not in the initial state")
	}
	d.socketSendCmd(sockn, sockCmdListen)
	return nil
}

// Accept waits for an incoming connection. The established hardware socket is
// handed to the caller and the listening socket is reopened on a free one.
func (d *Device) Accept(sockfd int) (int, netip.AddrPort, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	lsock, err := d.socket(sockfd)
	if err != nil {
		return -1, netip.AddrPort{}, errors.New("w6300: could not get socket: " + err.Error())
	}

	if err = d.waitForEstablished(lsock.sockn); err != nil {
		return -1, netip.AddrPort{}, err
	}

	csockfd, csock, err := d.nextSocket()
	if err != nil {
		return -1, netip.AddrPort{}, err
	}
	// Swap the socket numbers: the accepted socket becomes the client and
	// the listening socket moves to the free hardware socket.
	lsock.sockn, csock.sockn = csock.sockn, lsock.sockn

	d.openSocket(lsock.sockn, lsock.protocol)
	if err = d.bindSocket(lsock.sockn, lsock.port); err != nil {
		return -1, netip.AddrPort{}, errors.New("w6300: could not bind listening socket: " + err.Error())
	}
	if err = d.listen(lsock.sockn); err != nil {
		return -1, netip.AddrPort{}, errors.New("w6300: could not set listening socket: " + err.Error())
	}

	csock.setInUse(true)

	return csockfd, d.remoteAddr(csock.sockn), nil
}

func (d *Device) waitForEstablished(sockn uint8) error {
	for {
		switch d.sockStatus(sockn) {
		case sockStatusEstablished:
			return nil
		case sockStatusClosed:
			return net.ErrClosed
		case sockStatusCloseWait:
			// The peer closed before we accepted; re-listen.
			if err := d.listen(sockn); err != nil {
				return errors.New("w6300: could not set socket to listen: " + err.Error())
			}
		}
		d.irqPoll(sockn, sockIntConnect|sockIntDisconnect, time.Time{})
	}
}

func (d *Device) remoteAddr(sockn uint8) netip.AddrPort {
	var rip [4]byte
	d.read(sockDestIP, sockAddr(sockn), rip[:])

	var rport [2]byte
	d.read(sockDestPort, sockAddr(sockn), rport[:])

	return netip.AddrPortFrom(netip.AddrFrom4(rip), uint16(rport[0])<<8|uint16(rport[1]))
}

// Send sends data, blocking until all of it is handed to the controller or the
// deadline is reached.
func (d *Device) Send(sockfd int, buf []byte, _ int, deadline time.Time) (int, error) {
	bufLen := len(buf)
	if bufLen <= d.maxSockSize {
		return d.sendChunk(sockfd, buf, deadline)
	}

	var n int
	for i := 0; i < bufLen; i += d.maxSockSize {
		end := min(i+d.maxSockSize, bufLen)

		sent, err := d.sendChunk(sockfd, buf[i:end], deadline)
		if err != nil {
			return n, errors.New("w6300: could not send chunk: " + err.Error())
		}
		n += sent
	}
	return n, nil
}

func (d *Device) sendChunk(sockfd int, buf []byte, deadline time.Time) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	sock, err := d.socket(sockfd)
	if err != nil {
		return 0, errors.New("w6300: could not get socket: " + err.Error())
	}
	if sock.closed {
		return 0, os.ErrClosed
	}

	bufLen := uint16(len(buf))
	if err = d.waitForFreeBuffer(sock.sockn, bufLen, deadline); err != nil {
		return 0, err
	}

	sendPtr := d.readUint16(sockTXWritePtr, sockAddr(sock.sockn))

	d.write(sendPtr, bufferAddr(sock.sockn, blockTX), buf)
	d.writeUint16(sockTXWritePtr, sockAddr(sock.sockn), sendPtr+bufLen)
	d.socketSendCmd(sock.sockn, sockCmdSend)

	irq := d.irqPoll(sock.sockn, sockIntSendOK|sockIntDisconnect|sockIntTimeout, deadline)
	switch {
	case irq == sockIntUnknown:
		return 0, os.ErrDeadlineExceeded
	case irq&sockIntSendOK != 0:
		// The peer may close while the SEND is completing; the bytes are
		// already on the wire, so a simultaneous DISCON must not turn a
		// successful send into net.ErrClosed.
		return int(bufLen), nil
	case irq&sockIntDisconnect != 0:
		sock.setClosed(true)
		return 0, net.ErrClosed
	case irq&sockIntTimeout != 0:
		return 0, netdev.ErrTimeout
	default:
		return int(bufLen), nil
	}
}

func (d *Device) waitForFreeBuffer(sockn uint8, size uint16, deadline time.Time) error {
	for {
		if d.readUint16(sockTXFreeSize, sockAddr(sockn)) >= size {
			return nil
		}

		if !deadline.IsZero() && time.Now().After(deadline) {
			return netdev.ErrTimeout
		}

		switch d.sockStatus(sockn) {
		case sockStatusEstablished, sockStatusCloseWait, sockStatusUDP:
		default:
			return errors.New("w6300: socket is not in a valid state for sending data")
		}

		d.mu.Unlock()
		time.Sleep(time.Millisecond)
		d.mu.Lock()
	}
}

// Recv reads available data into buf.
func (d *Device) Recv(sockfd int, buf []byte, _ int, deadline time.Time) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	sock, err := d.socket(sockfd)
	if err != nil {
		return 0, errors.New("w6300: could not get socket: " + err.Error())
	}
	if sock.closed {
		return 0, os.ErrClosed
	}

	size, err := d.waitForData(sock, deadline)
	if err != nil {
		return 0, err
	}

	recvPtr := d.readUint16(sockRXReadPtr, sockAddr(sock.sockn))

	buf = buf[:min(size, len(buf))]
	d.read(recvPtr, bufferAddr(sock.sockn, blockRX), buf)
	d.writeUint16(sockRXReadPtr, sockAddr(sock.sockn), recvPtr+uint16(len(buf)))
	d.socketSendCmd(sock.sockn, sockCmdRecv)

	return len(buf), nil
}

func (d *Device) waitForData(sock *socket, deadline time.Time) (int, error) {
	for {
		received := d.readUint16(sockRXRecvSize, sockAddr(sock.sockn))
		if received > 0 {
			return int(received), nil
		}

		irq := d.irqPoll(sock.sockn, sockIntReceive|sockIntDisconnect, deadline)
		switch {
		case irq == sockIntUnknown:
			return 0, os.ErrDeadlineExceeded
		case irq&sockIntDisconnect != 0:
			sock.setClosed(true)
			return 0, net.ErrClosed
		}
	}
}

// Close closes the socket.
func (d *Device) Close(sockfd int) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	sock, err := d.socket(sockfd)
	if err != nil {
		return err
	}

	d.socketSendCmd(sock.sockn, sockCmdClose)
	sock.reset()
	return nil
}

func (d *Device) nextSocket() (int, *socket, error) {
	for i, sock := range d.sockets {
		if sock.inUse {
			continue
		}
		return i, sock, nil
	}
	return -1, nil, netdev.ErrNoMoreSockets
}

func (d *Device) socket(sockfd int) (*socket, error) {
	if sockfd < 0 || sockfd >= len(d.sockets) {
		return nil, netdev.ErrInvalidSocketFd
	}
	return d.sockets[sockfd], nil
}

func (d *Device) socketSendCmd(sockn uint8, cmd byte) {
	d.writeByte(sockCmd, sockAddr(sockn), cmd)
	for d.readByte(sockCmd, sockAddr(sockn)) != 0 {
		runtime.Gosched()
	}
}

func (d *Device) sockStatus(sockn uint8) int {
	return int(d.readByte(sockStatus, sockAddr(sockn)))
}
