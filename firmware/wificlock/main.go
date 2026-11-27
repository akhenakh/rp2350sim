// wificlock joins the virtual AP, gets an address over DHCP, resolves
// pool.ntp.org, sets the clock from the NTP reply and shows UTC on an ST7789
// panel. It is the Wi-Fi + display integration example for the Pico 2 W.
package main

import (
	"image/color"
	"machine"
	"net/netip"
	"runtime"
	"time"

	"github.com/soypat/cyw43439"
	"github.com/soypat/cyw43439/examples/cywnet"
	"github.com/soypat/lneto/dns"
	"github.com/soypat/lneto/ipv4"
	"tinygo.org/x/drivers"
	"tinygo.org/x/drivers/st7789"
	"tinygo.org/x/tinyfont"
	"tinygo.org/x/tinyfont/freesans"
)

const (
	ssid    = "rp2350sim"
	pass    = "password"
	ntpHost = "pool.ntp.org"

	lcdRST = machine.GPIO20
	lcdDC  = machine.GPIO16
	lcdCS  = machine.GPIO17
	lcdBL  = machine.GPIO15
)

var (
	bg      = color.RGBA{8, 12, 24, 255}
	fg      = color.RGBA{220, 240, 255, 255}
	accent  = color.RGBA{80, 200, 160, 255}
	ntpName = dns.MustNewName(ntpHost)

	// clockBuf batches one clock/date region into a single SPI transfer.
	// Drawing tinyfont straight onto the panel costs a window set and a write
	// per pixel and dominates the frame time.
	clockBuf [320 * 72 * 2]byte
)

type tile struct {
	x, y, w, h int16
	buf        []byte
}

func (t *tile) Size() (int16, int16) { return 320, 240 }
func (t *tile) Display() error       { return nil }

func rgb565(c color.RGBA) (byte, byte) {
	v := uint16(c.R&0xF8)<<8 | uint16(c.G&0xFC)<<3 | uint16(c.B)>>3
	return byte(v >> 8), byte(v)
}

func (t *tile) SetPixel(x, y int16, c color.RGBA) {
	x, y = x-t.x, y-t.y
	if x < 0 || y < 0 || x >= t.w || y >= t.h {
		return
	}
	i := (int(y)*int(t.w) + int(x)) * 2
	t.buf[i], t.buf[i+1] = rgb565(c)
}

func paint(d *st7789.Device, x, y, w, h int16, bgc color.RGBA, draw func(*tile)) {
	t := tile{x: x, y: y, w: w, h: h, buf: clockBuf[:int(w)*int(h)*2]}
	hi, lo := rgb565(bgc)
	t.buf[0], t.buf[1] = hi, lo
	for n := 2; n < len(t.buf); n *= 2 {
		copy(t.buf[n:], t.buf[:n])
	}
	draw(&t)
	_ = d.DrawRGBBitmap8(x, y, t.buf, w, h)
}

func main() {
	time.Sleep(500 * time.Millisecond)
	println("wificlock: init")
	display := st7789.New(machine.SPI0, lcdRST, lcdDC, lcdCS, lcdBL)
	display.Configure(st7789.Config{Width: 240, Height: 320, Rotation: drivers.Rotation90})
	drawBoot(&display, "Wi-Fi...")

	cystack, err := cywnet.NewConfiguredPicoWithStack(ssid, pass, cyw43439.DefaultWifiConfig(), cywnet.StackConfig{
		Hostname:          "wificlock",
		MaxActiveUDPPorts: 1,
	})
	if err != nil {
		fail(&display, "Wi-Fi setup", err)
	}
	go loop(cystack)

	drawBoot(&display, "DHCP...")
	dhcp, err := cystack.SetupWithDHCP(cywnet.DHCPConfig{})
	if err != nil {
		fail(&display, "DHCP", err)
	}
	ip := ipv4.String(dhcp.AssignedAddr4)
	println("wificlock: dhcp", ip)

	drawBoot(&display, "NTP...")
	stack := cystack.StackAsync()
	rstack := stack.StackRetrying(cywnet.DefaultStackBackoff)
	var addrs [1]netip.Addr
	if _, err := rstack.DoLookupIP(addrs[:], ntpName, 5*time.Second, 3); err != nil {
		fail(&display, "DNS "+ntpHost, err)
	}
	println("wificlock: dns", addrs[0].String())

	offset, err := rstack.DoNTP(addrs[0], 5*time.Second, 3)
	if err != nil {
		fail(&display, "NTP", err)
	}
	runtime.AdjustTimeOffset(int64(offset))
	println("wificlock: ntp", time.Now().UTC().Format(time.RFC3339), "offset", offset.String())
	drawStatic(&display, ip, addrs[0].String())
	updateClock(&display, time.Now().UTC())

	for {
		updateClock(&display, time.Now().UTC())
		time.Sleep(250 * time.Millisecond)
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

func drawBoot(d *st7789.Device, msg string) {
	_ = d.FillRectangle(0, 0, 320, 240, bg)
	_ = d.FillRectangle(0, 0, 320, 4, accent)
	tinyfont.WriteLine(d, &freesans.Bold18pt7b, 20, 60, "NTP CLOCK", fg)
	tinyfont.WriteLine(d, &freesans.Regular12pt7b, 20, 110, msg, accent)
	tinyfont.WriteLine(d, &freesans.Regular9pt7b, 20, 200, ntpHost, fg)
}

var (
	timeCells = [...]struct {
		x, w int16
	}{
		{70, 26}, {96, 26}, {122, 12}, {134, 26},
		{160, 26}, {186, 12}, {198, 26}, {224, 26},
	}
	prevTime string
	prevDate string
)

func drawStatic(d *st7789.Device, ip, server string) {
	_ = d.FillRectangle(0, 0, 320, 240, bg)
	_ = d.FillRectangle(0, 0, 320, 4, accent)
	tinyfont.WriteLine(d, &freesans.Bold18pt7b, 20, 38, "NTP CLOCK", fg)
	tinyfont.WriteLine(d, &freesans.Regular9pt7b, 20, 62, ntpHost+" · "+ip, accent)
	tinyfont.WriteLine(d, &freesans.Regular9pt7b, 20, 80, "server "+server, accent)
	tinyfont.WriteLine(d, &freesans.Regular9pt7b, 20, 230, "UTC", accent)
}

// drawTimeChar rewrites one character cell, leaving the rest of the panel and
// its background untouched. Only the digits that changed are sent each tick.
func drawTimeChar(d *st7789.Device, i int, r rune) {
	c := timeCells[i]
	paint(d, c.x, 116, c.w, 46, bg, func(t *tile) {
		tinyfont.DrawChar(t, &freesans.Bold24pt7b, c.x, 158, r, fg)
	})
}

func updateClock(d *st7789.Device, now time.Time) {
	stamp := now.Format("15:04:05")
	for i := 0; i < len(stamp) && i < len(timeCells); i++ {
		if i >= len(prevTime) || prevTime[i] != stamp[i] {
			drawTimeChar(d, i, rune(stamp[i]))
		}
	}
	prevTime = stamp
	if date := now.Format("Mon 02 Jan 2006"); date != prevDate {
		paint(d, 8, 176, 304, 34, bg, func(t *tile) {
			tinyfont.WriteLine(t, &freesans.Regular12pt7b, 46, 205, date, fg)
		})
		prevDate = date
	}
}

func fail(d *st7789.Device, what string, err error) {
	println("wificlock:", what, "failed:", err.Error())
	_ = d.FillRectangle(0, 100, 320, 90, bg)
	tinyfont.WriteLine(d, &freesans.Regular12pt7b, 20, 130, what+" failed", color.RGBA{255, 100, 100, 255})
	tinyfont.WriteLine(d, &freesans.Regular9pt7b, 20, 160, err.Error(), fg)
	for {
		time.Sleep(time.Second)
	}
}
