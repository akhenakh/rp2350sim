// Package w6300 implements a driver for the WIZnet W6300 Ethernet controller.
//
// The W6300 is a hardwired dual TCP/IP stack with eight sockets, exposed over
// a QSPI interface. This driver talks to it in single SPI mode: an instruction
// byte (mode bits, read/write, block select), a 16-bit offset, one dummy byte,
// then the data stream. The high-level API matches the netdev driver used by
// TinyGo's net package.
//
// Datasheet: https://docs.wiznet.io/img/products/w6300/W6300_ds_v1.0.0.pdf
package w6300

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"

	"tinygo.org/x/drivers"
	"tinygo.org/x/drivers/netdev"
)

var _ netdev.Netdever = &Device{}

// Chip identification reported by CIDR0..CIDR2.
const (
	ChipMajor = 0x6100
	ChipMinor = 0x11
)

// Resolver is a function that resolves a hostname to an IP address.
type Resolver func(host string) (netip.Addr, error)

// CSPin is the chip-select output. machine.Pin satisfies it.
type CSPin interface {
	Set(high bool)
}

// Device is a driver for the W6300 Ethernet controller.
type Device struct {
	maxSockets  int
	maxSockSize int

	mu  sync.Mutex
	bus drivers.SPI
	cs  func(bool)
	dns Resolver

	sockets []*socket
	laddr   netip.Addr

	cmdBuf [4]byte
}

// New returns a new W6300 driver. The SPI bus must be fully configured before
// Configure is called.
func New(bus drivers.SPI, csPin CSPin) *Device {
	return &Device{
		bus: bus,
		cs:  csPin.Set,
	}
}

// Config is the configuration for the device.
type Config struct {
	DNS Resolver

	MAC        net.HardwareAddr
	IP         netip.Addr
	SubnetMask netip.Addr
	Gateway    netip.Addr

	// Optional, default is 8.
	MaxSockets int
}

// Configure resets the device and programs the network configuration. The MAC
// address is required; the IP fields are optional.
func (d *Device) Configure(cfg Config) error {
	if len(cfg.MAC) != 6 {
		return errors.New("w6300: MAC address must be 6 bytes")
	}

	d.cs(true)

	d.mu.Lock()
	defer d.mu.Unlock()

	d.dns = cfg.DNS

	d.reset()

	if err := d.setupSockets(cfg.MaxSockets); err != nil {
		return errors.New("w6300: could not setup sockets: " + err.Error())
	}

	d.write(regSHAR, 0, cfg.MAC)
	d.write(regSIPR, 0, cfg.IP.AsSlice())
	d.write(regSUBR, 0, cfg.SubnetMask.AsSlice())
	d.write(regGAR, 0, cfg.Gateway.AsSlice())
	d.laddr = cfg.IP
	return nil
}

func (d *Device) setupSockets(maxSockets int) error {
	if maxSockets == 0 {
		maxSockets = 8
	}
	switch maxSockets {
	case 1, 2, 4, 8:
	default:
		return errors.New("invalid number of sockets, must be one of 1, 2, 4, or 8")
	}

	socks := make([]*socket, maxSockets)
	for i := range socks {
		socks[i] = &socket{sockn: uint8(i)}
	}

	// The W6300 has 32 KB of TX and 32 KB of RX memory shared between the
	// sockets; each Sn_TX_BSR/Sn_RX_BSR unit is 1 KB.
	d.maxSockets = maxSockets
	d.maxSockSize = 32 * 1024 / maxSockets
	d.sockets = socks

	for i := 0; i < 8; i++ {
		size := byte(d.maxSockSize >> 10)
		if i >= maxSockets {
			size = 0
		}
		d.writeByte(sockTXBufSize, sockAddr(uint8(i)), size)
		d.writeByte(sockRXBufSize, sockAddr(uint8(i)), size)
	}

	d.writeByte(regSIMR, 0, 0xFF)
	d.writeByte(regIMR, 0, 0)
	return nil
}

// reset performs a software reset. SYCR0[RST] is active low: writing 0 resets
// the chip, writing 1 returns it to normal operation.
func (d *Device) reset() {
	d.writeByte(regSYCR0, 0, 0x00)
	time.Sleep(time.Millisecond)
	d.writeByte(regSYCR0, 0, 0x80)
	time.Sleep(time.Millisecond)
}

// Reset performs a soft reset.
func (d *Device) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reset()
}

// ChipID returns the chip identification registers, which are 0x6100 and 0x11
// on a W6300.
func (d *Device) ChipID() (uint16, uint8) {
	d.mu.Lock()
	defer d.mu.Unlock()
	major := uint16(d.readByte(regCIDR0, 0))<<8 | uint16(d.readByte(regCIDR1, 0))
	return major, d.readByte(regCIDR2, 0)
}

// GetHardwareAddr returns the hardware address of the device.
func (d *Device) GetHardwareAddr() (net.HardwareAddr, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	mac := make([]byte, 6)
	d.read(regSHAR, 0, mac)
	return mac, nil
}

// Addr returns the IP address of the device.
func (d *Device) Addr() (netip.Addr, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var ip [4]byte
	d.read(regSIPR, 0, ip[:])
	return netip.AddrFrom4(ip), nil
}

// SetAddr sets the IP address of the device.
func (d *Device) SetAddr(ip netip.Addr) error {
	return d.setAddress(regSIPR, ip)
}

// SetSubnetMask sets the subnet mask of the device.
func (d *Device) SetSubnetMask(mask netip.Addr) error {
	return d.setAddress(regSUBR, mask)
}

// SetGateway sets the gateway address of the device.
func (d *Device) SetGateway(gateway netip.Addr) error {
	return d.setAddress(regGAR, gateway)
}

func (d *Device) setAddress(addr uint16, ip netip.Addr) error {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.Is4() {
		return errors.New("w6300: invalid IP address: " + ip.String())
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.write(addr, 0, ip.AsSlice())
	return nil
}

// LinkStatus is the link status of the device.
type LinkStatus = uint8

// LinkStatus values.
const (
	LinkStatusDown LinkStatus = iota
	LinkStatusUp
)

// LinkStatus returns the current link status.
func (d *Device) LinkStatus() LinkStatus {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.readByte(regPHYSR, 0) & 0b00000001
}

// LinkInfo returns the negotiated speed and duplex.
func (d *Device) LinkInfo() string {
	d.mu.Lock()
	defer d.mu.Unlock()

	phy := d.readByte(regPHYSR, 0)
	speed := "100Mbps"
	if phy&0b00000010 != 0 {
		speed = "10Mbps"
	}
	duplex := "Full Duplex"
	if phy&0b00000100 != 0 {
		duplex = "Half Duplex"
	}
	if phy&0b00000001 == 0 {
		return "Link Down"
	}
	return speed + " " + duplex
}
