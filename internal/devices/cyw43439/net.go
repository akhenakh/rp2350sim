//go:build !js

package cyw43439

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	etherARP  = 0x0806
	etherIPv4 = 0x0800
	udpProto  = 17
)

// network is the gVisor stack behind the virtual AP. It accepts the guest's
// Ethernet frames at L3 and NATs TCP/UDP to the host; DHCP, DNS and NTP are
// answered locally because gVisor has no server for them.
type network struct {
	d      *Device
	cfg    *NetConfig
	log    *slog.Logger
	netLog *slog.Logger
	clk    *vclock
	s      *stack.Stack
	ep     *channel.Endpoint
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	socks  []*natSocket
	done   bool
}

// natSocket is one guest TCP/UDP flow as the Wi-Fi tab shows it.
type natSocket struct {
	proto string
	guest string
	peer  string
	port  int
	state string
}

func newDataPath(d *Device) (dataPath, error) {
	n := &network{
		d:      d,
		cfg:    d.cfg.Network,
		log:    d.log,
		netLog: loggerOr(d.cfg.Network.NetLogger),
		clk:    &vclock{},
		conns:  map[net.Conn]struct{}{},
	}
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
		Clock:              n.clk,
	})
	ep := channel.New(256, 1500, "")
	if err := s.CreateNIC(1, ep); err != nil {
		return nil, errors.New(err.String())
	}
	if err := s.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddrFrom4(n.cfg.GatewayIP).WithPrefix(),
	}, stack.AddressProperties{}); err != nil {
		return nil, errors.New(err.String())
	}
	// The guest may address any destination; the forwarders do the NAT.
	s.SetPromiscuousMode(1, true)
	s.SetSpoofing(1, true)
	s.SetRouteTable([]tcpip.Route{{
		Destination: subnetOf(n.cfg.GatewayIP, n.cfg.Prefix),
		NIC:         1,
	}})
	tcpFwd := tcp.NewForwarder(s, 0, 128, n.tcpHandler)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)
	udpFwd := udp.NewForwarder(s, n.udpHandler)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)
	n.s = s
	n.ep = ep
	return n, nil
}

func subnetOf(ip [4]byte, prefix int) tcpip.Subnet {
	addr := tcpip.AddrFrom4(ip)
	mask := net.CIDRMask(prefix, 32)
	sn, err := tcpip.NewSubnet(addr, tcpip.MaskFromBytes(mask))
	if err != nil {
		sn, _ = tcpip.NewSubnet(tcpip.AddrFrom4([4]byte{}), tcpip.MaskFromBytes(net.CIDRMask(0, 32)))
	}
	return sn
}

func (n *network) tick(micros uint64) {
	n.clk.advance(time.Duration(micros) * time.Microsecond)
	n.pollOutbound()
}

func (n *network) close() {
	n.mu.Lock()
	if n.done {
		n.mu.Unlock()
		return
	}
	n.done = true
	n.socks = nil
	conns := make([]net.Conn, 0, len(n.conns))
	for c := range n.conns {
		conns = append(conns, c)
	}
	n.conns = map[net.Conn]struct{}{}
	n.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	n.ep.Close()
	n.s.Close()
}

// networkInfo builds the Wi-Fi tab's view of the virtual AP.
func (n *network) networkInfo() devices.NetworkInfo {
	info := devices.NetworkInfo{
		Kind:       "CYW43439",
		Configured: n.d.LinkUp(),
		IP:         net.IP(n.cfg.GuestIP[:]).String(),
		Mask:       net.IP(net.CIDRMask(n.cfg.Prefix, 32)).String(),
		Gateway:    net.IP(n.cfg.GatewayIP[:]).String(),
		MAC:        net.HardwareAddr(n.d.cfg.MAC[:]).String(),
		Mode:       "station",
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for i, s := range n.socks {
		info.Sockets = append(info.Sockets, devices.SocketInfo{
			Index:     i,
			Proto:     s.proto,
			State:     s.state,
			LocalPort: s.port,
			GuestAddr: s.guest,
			Peer:      s.peer,
		})
	}
	return info
}

func guestSocket(id stack.TransportEndpointID) string {
	return net.JoinHostPort(net.IP(id.RemoteAddress.AsSlice()).String(), strconv.Itoa(int(id.RemotePort)))
}

func (n *network) addSocket(s *natSocket) {
	n.mu.Lock()
	if !n.done {
		n.socks = append(n.socks, s)
	}
	n.mu.Unlock()
}

func (n *network) setSocketState(s *natSocket, state string) {
	n.mu.Lock()
	s.state = state
	n.mu.Unlock()
}

func (n *network) removeSocket(s *natSocket) {
	n.mu.Lock()
	for i, x := range n.socks {
		if x == s {
			n.socks = append(n.socks[:i], n.socks[i+1:]...)
			break
		}
	}
	n.mu.Unlock()
}

// ---- Ethernet in/out ----

func (n *network) handleFrame(frame []byte) {
	if len(frame) < 14 {
		return
	}
	n.d.capture(true, frame)
	var src [6]byte
	copy(src[:], frame[6:12])
	switch binary.BigEndian.Uint16(frame[12:]) {
	case etherARP:
		n.handleARP(frame, src)
	case etherIPv4:
		ip := frame[14:]
		if len(ip) < 20 || ip[0]>>4 != 4 || int(ip[0]&0xf)*4 > len(ip) {
			return
		}
		if ip[9] == udpProto && n.handleUDPFrame(ip) {
			return
		}
		n.inject(ip)
	}
}

func (n *network) inject(ip []byte) {
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(append([]byte(nil), ip...)),
	})
	defer pkt.DecRef()
	n.ep.InjectInbound(ipv4.ProtocolNumber, pkt)
}

func (n *network) pollOutbound() {
	for {
		pkt := n.ep.Read()
		if pkt == nil {
			return
		}
		ip := pkt.ToView().AsSlice()
		pkt.DecRef()
		if len(ip) < 20 {
			continue
		}
		frame := make([]byte, 14+len(ip))
		copy(frame[0:6], n.d.cfg.MAC[:])
		copy(frame[6:12], n.cfg.APMAC[:])
		frame[12] = 0x08
		frame[13] = 0x00
		copy(frame[14:], ip)
		n.d.queueDataFrame(frame)
	}
}

// handleARP answers neighbor requests for the gateway/DNS addresses (and any
// other on-link address) with the virtual AP's MAC.
func (n *network) handleARP(frame []byte, src [6]byte) {
	p := frame[14:]
	if len(p) < 28 || binary.BigEndian.Uint16(p[6:]) != 1 { // request
		return
	}
	var target [4]byte
	copy(target[:], p[24:28])
	var reply [28]byte
	binary.BigEndian.PutUint16(reply[0:], 1)      // htype
	binary.BigEndian.PutUint16(reply[2:], 0x0800) // ptype
	reply[4] = 6
	reply[5] = 4
	binary.BigEndian.PutUint16(reply[6:], 2) // reply
	copy(reply[8:14], n.cfg.APMAC[:])
	copy(reply[14:18], target[:])
	copy(reply[18:24], src[:])
	copy(reply[24:28], p[14:18])
	out := make([]byte, 14+len(reply))
	copy(out[0:6], src[:])
	copy(out[6:12], n.cfg.APMAC[:])
	out[12] = 0x08
	out[13] = 0x06
	copy(out[14:], reply[:])
	n.d.queueDataFrame(out)
}

// ---- UDP responders ----

func (n *network) handleUDPFrame(ip []byte) bool {
	ihl := int(ip[0]&0xf) * 4
	if len(ip) < ihl+8 {
		return false
	}
	udp := ip[ihl:]
	srcPort := binary.BigEndian.Uint16(udp[0:])
	dstPort := binary.BigEndian.Uint16(udp[2:])
	ul := int(binary.BigEndian.Uint16(udp[4:]))
	if ul < 8 || ul > len(udp) {
		return false
	}
	payload := udp[8:ul]
	var srcIP, dstIP [4]byte
	copy(srcIP[:], ip[12:16])
	copy(dstIP[:], ip[16:20])
	switch dstPort {
	case 67:
		n.handleDHCP(srcIP, srcPort, dstIP, dstPort, payload)
		return true
	case 53:
		if n.d.debug {
			n.log.Debug("cyw43439 dns query", slog.String("src", net.IP(srcIP[:]).String()),
				slog.Int("len", len(payload)))
		}
		n.handleDNS(srcIP, srcPort, dstIP, dstPort, payload)
		return true
	case 123:
		n.handleNTP(srcIP, srcPort, dstIP, dstPort, payload)
		return true
	}
	return false
}

// udpFrame builds an Ethernet/IPv4/UDP frame from the virtual AP.
func (n *network) udpFrame(dstMAC [6]byte, srcIP, dstIP [4]byte, srcPort, dstPort uint16, payload []byte) []byte {
	udp := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint16(udp[0:], srcPort)
	binary.BigEndian.PutUint16(udp[2:], dstPort)
	binary.BigEndian.PutUint16(udp[4:], uint16(len(udp)))
	copy(udp[8:], payload)
	binary.BigEndian.PutUint16(udp[6:], udpChecksum(srcIP, dstIP, udp))

	ip := make([]byte, 20+len(udp))
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:], uint16(len(ip)))
	binary.BigEndian.PutUint16(ip[4:], uint16(n.nextIPID()))
	binary.BigEndian.PutUint16(ip[6:], 0x4000) // DF
	ip[8] = 64
	ip[9] = udpProto
	copy(ip[12:16], srcIP[:])
	copy(ip[16:20], dstIP[:])
	binary.BigEndian.PutUint16(ip[10:], ipChecksum(ip[:20]))
	copy(ip[20:], udp)

	frame := make([]byte, 14+len(ip))
	copy(frame[0:6], dstMAC[:])
	copy(frame[6:12], n.cfg.APMAC[:])
	frame[12] = 0x08
	frame[13] = 0x00
	copy(frame[14:], ip)
	return frame
}

var ipID struct {
	sync.Mutex
	n uint16
}

func (n *network) nextIPID() uint16 {
	ipID.Lock()
	ipID.n++
	v := ipID.n
	ipID.Unlock()
	return v
}

func ipChecksum(hdr []byte) uint16 {
	var sum uint32
	for i := 0; i+2 <= len(hdr); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(hdr[i:]))
	}
	if len(hdr)%2 != 0 {
		sum += uint32(hdr[len(hdr)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

func udpChecksum(src, dst [4]byte, udp []byte) uint16 {
	var sum uint32
	add := func(p []byte) {
		for i := 0; i+2 <= len(p); i += 2 {
			sum += uint32(binary.BigEndian.Uint16(p[i:]))
		}
		if len(p)%2 != 0 {
			sum += uint32(p[len(p)-1]) << 8
		}
	}
	add(src[:])
	add(dst[:])
	sum += uint32(udpProto)
	sum += uint32(len(udp))
	add(udp)
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	if v := ^uint16(sum); v != 0 {
		return v
	}
	return 0xffff
}

// ---- TCP/UDP NAT ----

func (n *network) tcpHandler(r *tcp.ForwarderRequest) {
	if n.isDone() {
		r.Complete(true)
		return
	}
	id := r.ID()
	dst := id.LocalAddress
	port := id.LocalPort
	var wq waiter.Queue
	ep, err := r.CreateEndpoint(&wq)
	if err != nil {
		r.Complete(true)
		return
	}
	r.Complete(false)
	guest := gonet.NewTCPConn(&wq, ep)
	sock := &natSocket{
		proto: "tcp",
		guest: guestSocket(id),
		peer:  net.JoinHostPort(dst.String(), strconv.Itoa(int(port))),
		port:  int(port),
		state: "dialing",
	}
	n.addSocket(sock)
	n.track(guest)
	n.netLog.Info("tcp connect", "guest", sock.guest, "peer", sock.peer)
	go func() {
		host, err := net.DialTimeout("tcp", net.JoinHostPort(n.hostAddr(dst), strconv.Itoa(int(port))), 5*time.Second)
		if err != nil {
			if n.log != nil {
				n.log.Debug("cyw43439 tcp dial", slog.String("dst", dst.String()),
					slog.Int("port", int(port)), slog.Any("err", err))
			}
			n.netLog.Info("tcp failed", "peer", sock.peer, "err", err)
			n.setSocketState(sock, "failed")
			_ = guest.Close()
			n.untrack(guest)
			time.AfterFunc(2*time.Second, func() { n.removeSocket(sock) })
			return
		}
		n.setSocketState(sock, "connected")
		n.netLog.Info("tcp connected", "guest", sock.guest, "peer", sock.peer)
		n.track(host)
		relay(guest, host)
		n.setSocketState(sock, "closed")
		n.netLog.Info("tcp closed", "guest", sock.guest, "peer", sock.peer)
		time.AfterFunc(2*time.Second, func() { n.removeSocket(sock) })
		n.untrack(guest)
		n.untrack(host)
	}()
}

func (n *network) udpHandler(r *udp.ForwarderRequest) bool {
	if n.isDone() {
		return true
	}
	id := r.ID()
	dst := id.LocalAddress
	port := id.LocalPort
	var wq waiter.Queue
	ep, err := r.CreateEndpoint(&wq)
	if err != nil {
		return false
	}
	guest := gonet.NewUDPConn(&wq, ep)
	sock := &natSocket{
		proto: "udp",
		guest: guestSocket(id),
		peer:  net.JoinHostPort(dst.String(), strconv.Itoa(int(port))),
		port:  int(port),
		state: "open",
	}
	n.addSocket(sock)
	n.track(guest)
	n.netLog.Info("udp open", "guest", sock.guest, "peer", sock.peer)
	go func() {
		host, err := net.Dial("udp", net.JoinHostPort(n.hostAddr(dst), strconv.Itoa(int(port))))
		if err != nil {
			n.netLog.Info("udp failed", "peer", sock.peer, "err", err)
			_ = guest.Close()
			n.untrack(guest)
			n.removeSocket(sock)
			return
		}
		n.track(host)
		relayUDP(guest, host)
		_ = guest.Close()
		_ = host.Close()
		n.setSocketState(sock, "closed")
		n.netLog.Info("udp closed", "guest", sock.guest, "peer", sock.peer)
		time.AfterFunc(2*time.Second, func() { n.removeSocket(sock) })
		n.untrack(guest)
		n.untrack(host)
	}()
	return true
}

// hostAddr maps the gateway and DNS addresses to the host loopback and leaves
// everything else as the literal destination (ordinary NAT).
func (n *network) hostAddr(dst tcpip.Address) string {
	b := dst.AsSlice()
	if len(b) == 4 {
		var ip [4]byte
		copy(ip[:], b)
		if ip == n.cfg.GatewayIP || ip == n.cfg.DNSIP {
			return "127.0.0.1"
		}
	}
	return net.IP(b).String()
}

func relay(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(a, b); closeBoth() }()
	go func() { defer wg.Done(); _, _ = io.Copy(b, a); closeBoth() }()
	wg.Wait()
}

func relayUDP(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer closeBoth()
		buf := make([]byte, 64*1024)
		for {
			n, err := a.Read(buf)
			if err != nil {
				return
			}
			if _, err := b.Write(buf[:n]); err != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		defer closeBoth()
		buf := make([]byte, 64*1024)
		for {
			n, err := b.Read(buf)
			if err != nil {
				return
			}
			if _, err := a.Write(buf[:n]); err != nil {
				return
			}
		}
	}()
	wg.Wait()
}

func (n *network) track(c net.Conn) {
	n.mu.Lock()
	if n.done {
		n.mu.Unlock()
		_ = c.Close()
		return
	}
	n.conns[c] = struct{}{}
	n.mu.Unlock()
}

func (n *network) untrack(c net.Conn) {
	n.mu.Lock()
	delete(n.conns, c)
	n.mu.Unlock()
}

func (n *network) isDone() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.done
}

// ---- virtual clock ----

var vclockEpoch = time.Unix(1, 0)

type vclock struct {
	mu     sync.Mutex
	now    time.Duration
	timers []*vtimer
}

type vtimer struct {
	clk     *vclock
	at      time.Duration
	f       func()
	stop    bool
	fired   bool
	inQueue bool
}

func (c *vclock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return vclockEpoch.Add(c.now)
}

func (c *vclock) NowMonotonic() tcpip.MonotonicTime {
	c.mu.Lock()
	defer c.mu.Unlock()
	return tcpip.MonotonicTime{}.Add(c.now)
}

func (c *vclock) AfterFunc(d time.Duration, f func()) tcpip.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &vtimer{clk: c, at: c.now + d, f: f, inQueue: true}
	c.timers = append(c.timers, t)
	return t
}

func (t *vtimer) Stop() bool {
	c := t.clk
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.stop || t.fired {
		return false
	}
	t.stop = true
	return true
}

// Reset re-arms the timer. A timer that already fired was dropped from the
// clock's queue, so it must be appended again or gVisor's retransmit and
// TIME_WAIT timers would silently never fire a second time.
func (t *vtimer) Reset(d time.Duration) {
	c := t.clk
	c.mu.Lock()
	defer c.mu.Unlock()
	t.at = c.now + d
	t.fired = false
	t.stop = false
	if !t.inQueue {
		c.timers = append(c.timers, t)
		t.inQueue = true
	}
}

func (c *vclock) advance(to time.Duration) {
	c.mu.Lock()
	if to > c.now {
		c.now = to
	}
	var due []func()
	live := c.timers[:0]
	for _, t := range c.timers {
		if t.stop || t.fired {
			t.inQueue = false
			continue
		}
		if t.at <= c.now {
			t.fired = true
			t.inQueue = false
			due = append(due, t.f)
			continue
		}
		live = append(live, t)
	}
	c.timers = live
	c.mu.Unlock()
	for _, f := range due {
		go f()
	}
}

// resolveHost resolves a DNS name through the board hosts map and then the
// host resolver.
func (n *network) resolveHost(name string) []net.IP {
	if v, ok := n.cfg.Hosts[name]; ok {
		if ip := net.ParseIP(v); ip != nil {
			return []net.IP{ip}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", name)
	if err != nil {
		return nil
	}
	return ips
}
