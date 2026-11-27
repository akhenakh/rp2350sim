// lab is the RP2350-Touch-LCD-2 dashboard for the timed devices: a DS18B20 on
// 1-Wire, a DHT22, an HC-SR04, an NMEA GPS on UART1 and an MCP2515 on SPI0.
// Each sensor is optional: a missing device leaves its row at n/a, so the same
// image runs on every lab board variant. ITM stimulus lines feed the Trace tab.
package main

import (
	"fmt"
	"image/color"
	"machine"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"tinygo.org/x/drivers"
	"tinygo.org/x/drivers/st7789"
	"tinygo.org/x/tinyfont"
	"tinygo.org/x/tinyfont/freesans"
)

const (
	lcdRST = machine.GPIO20
	lcdDC  = machine.GPIO16
	lcdCS  = machine.GPIO17
	lcdBL  = machine.GPIO15

	oneWirePin = machine.GPIO2
	dhtPin     = machine.GPIO3
	trigPin    = machine.GPIO5
	echoPin    = machine.GPIO6

	canCS  = machine.GPIO13
	canINT = machine.GPIO14

	itmStim0 = 0xE0000000
	itmTER   = 0xE0000E00
	itmTCR   = 0xE0000E80
)

var (
	uart = machine.UART0
	gps  = machine.UART1

	bg     = color.RGBA{R: 7, G: 10, B: 17, A: 255}
	panel  = color.RGBA{R: 16, G: 22, B: 34, A: 255}
	panel2 = color.RGBA{R: 13, G: 30, B: 38, A: 255}
	accent = color.RGBA{R: 79, G: 214, B: 255, A: 255}
	green  = color.RGBA{R: 70, G: 220, B: 140, A: 255}
	amber  = color.RGBA{R: 255, G: 176, B: 48, A: 255}
	red    = color.RGBA{R: 255, G: 72, B: 72, A: 255}
	white  = color.RGBA{R: 235, G: 240, B: 248, A: 255}
	muted  = color.RGBA{R: 120, G: 135, B: 158, A: 255}
)

type gpsFix struct {
	ok      bool
	lat     float64
	lon     float64
	sats    int
	last    string
	line    string
	updated uint32
}

var (
	oneTemp   int // hundredths of a degree Celsius
	oneOK     bool
	dhtHum    int // tenths
	dhtTemp   int // tenths
	dhtOK     bool
	distance  int
	echoOK    bool
	position  gpsFix
	canID     uint32
	canData   byte
	canOK     bool
	canTried  bool
	drawCount uint32

	drawnOneOK    bool
	drawnOneTemp  int
	drawnDhtOK    bool
	drawnDhtHum   int
	drawnDhtTemp  int
	drawnEchoOK   bool
	drawnDistance int
	drawnGPSOK    bool
	drawnGPSLat   float64
	drawnGPSLon   float64
	drawnGPSSats  int
	drawnCANOK    bool
	drawnCANTried bool
	drawnCANID    uint32
	drawnCANData  byte
)

func logf(format string, args ...any) {
	uart.Write([]byte(fmt.Sprintf(format, args...)))
	uart.Write([]byte("\r\n"))
}

func itm(s string) {
	if *unsafePointer(itmTER)&1 == 0 {
		return
	}
	for i := 0; i < len(s); i++ {
		for *unsafePointer(itmStim0) == 0 {
		}
		*(*uint8)(unsafe.Pointer(uintptr(itmStim0))) = s[i]
	}
}

// unsafePointer is a tiny helper so the ITM register constants stay readable.
func unsafePointer(addr uint32) *uint32 { return (*uint32)(unsafe.Pointer(uintptr(addr))) }

func f2(v int) string {
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}

func f1(v int) string { return fmt.Sprintf("%d.%d", v/10, v%10) }

// --- 1-Wire DS18B20 ---------------------------------------------------------

func owLow(us int) {
	oneWirePin.Configure(machine.PinConfig{Mode: machine.PinOutput})
	oneWirePin.Low()
	time.Sleep(time.Duration(us) * time.Microsecond)
}

func owRelease(us int) {
	oneWirePin.Configure(machine.PinConfig{Mode: machine.PinInputPullup})
	time.Sleep(time.Duration(us) * time.Microsecond)
}

func owReset() bool {
	owLow(500)
	owRelease(70)
	present := !oneWirePin.Get()
	owRelease(430)
	return present
}

func owWriteBit(bit byte) {
	if bit == 0 {
		owLow(60)
		owRelease(10)
		return
	}
	owLow(5)
	owRelease(65)
}

func owWriteByte(v byte) {
	for i := 0; i < 8; i++ {
		owWriteBit((v >> uint(i)) & 1)
	}
}

func owReadBit() byte {
	owLow(2)
	oneWirePin.Configure(machine.PinConfig{Mode: machine.PinInputPullup})
	time.Sleep(10 * time.Microsecond)
	bit := byte(0)
	if oneWirePin.Get() {
		bit = 1
	}
	time.Sleep(50 * time.Microsecond)
	return bit
}

func owReadByte() byte {
	var v byte
	for i := 0; i < 8; i++ {
		v |= owReadBit() << uint(i)
	}
	return v
}

func crc8(data []byte) byte {
	var crc byte
	for _, b := range data {
		crc ^= b
		for range 8 {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0x8C
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

func readOneWire() bool {
	if !owReset() {
		return false
	}
	owWriteByte(0xCC) // skip ROM
	owWriteByte(0x44) // convert T
	time.Sleep(750 * time.Millisecond)
	if !owReset() {
		return false
	}
	owWriteByte(0xCC)
	owWriteByte(0xBE) // read scratchpad
	var s [9]byte
	for i := range s {
		s[i] = owReadByte()
	}
	if crc8(s[:8]) != s[8] {
		return false
	}
	oneTemp = int(int16(uint16(s[1])<<8|uint16(s[0]))) * 100 / 16
	return true
}

// --- DHT22 -----------------------------------------------------------------

func dhtWait(level bool, maxUS int) bool {
	start := time.Now()
	for time.Since(start) < time.Duration(maxUS)*time.Microsecond {
		if dhtPin.Get() == level {
			return true
		}
	}
	return false
}

func spin(us int) {
	start := time.Now()
	for time.Since(start) < time.Duration(us)*time.Microsecond {
	}
}

func readDHT() bool {
	dhtPin.Configure(machine.PinConfig{Mode: machine.PinOutput})
	dhtPin.Low()
	spin(2000) // the model answers at >= 1 ms; leave margin for the next read
	dhtPin.Configure(machine.PinConfig{Mode: machine.PinInputPullup})
	if !dhtWait(false, 100) {
		logf("lab dht: no low1")
		return false
	}
	if !dhtWait(true, 100) {
		logf("lab dht: no high1")
		return false
	}
	if !dhtWait(false, 100) {
		logf("lab dht: no low2")
		return false
	}
	var data [5]byte
	for i := range 39 {
		if !dhtWait(true, 100) {
			return false
		}
		n := 0
		for dhtPin.Get() && n < 100000 {
			n++
		}
		bit := byte(0)
		if n > 1000 {
			bit = 1
		}
		data[i/8] |= bit << uint(i%8)
		if !dhtWait(false, 100) {
			return false
		}
	}
	var sum byte
	for _, b := range data[:4] {
		sum += b
	}
	if data[4]&0x7f != sum&0x7f {
		return false
	}
	dhtHum = int(uint16(data[0])<<8 | uint16(data[1]))
	dhtTemp = int(int16(uint16(data[2])<<8 | uint16(data[3])))
	return true
}

// --- HC-SR04 ---------------------------------------------------------------

func readDistance() bool {
	trigPin.High()
	time.Sleep(10 * time.Microsecond)
	trigPin.Low()
	start := time.Now()
	for !echoPin.Get() && time.Since(start) < 10*time.Millisecond {
	}
	if !echoPin.Get() {
		return false
	}
	rise := time.Now()
	for echoPin.Get() && time.Since(rise) < 30*time.Millisecond {
	}
	distance = int((time.Since(rise).Microseconds() + 29) / 58)
	return true
}

// --- GPS NMEA --------------------------------------------------------------

func nmeaChecksum(line string) bool {
	star := strings.LastIndexByte(line, '*')
	if star < 0 || star+2 >= len(line) {
		return false
	}
	var sum byte
	for i := 1; i < star; i++ {
		sum ^= line[i]
	}
	want, err := strconv.ParseUint(line[star+1:star+3], 16, 8)
	return err == nil && sum == byte(want)
}

func nmeaCoord(v, hemi string) float64 {
	x, _ := strconv.ParseFloat(v, 64)
	deg := float64(int(x / 100))
	out := deg + (x-deg*100)/60
	if hemi == "S" || hemi == "W" {
		out = -out
	}
	return out
}

func pumpGPS() {
	for gps.Buffered() > 0 {
		b, err := gps.ReadByte()
		if err != nil {
			return
		}
		if b != '\n' {
			if b != '\r' && len(position.line) < 100 {
				position.line += string(b)
			}
			continue
		}
		line := position.line
		position.line = ""
		if !strings.HasPrefix(line, "$GPGGA,") || !nmeaChecksum(line) {
			continue
		}
		f := strings.Split(line, ",")
		if len(f) < 10 {
			continue
		}
		position.ok = f[6] != "0" && f[6] != ""
		position.lat = nmeaCoord(f[2], f[3])
		position.lon = nmeaCoord(f[4], f[5])
		position.sats, _ = strconv.Atoi(f[7])
		position.last = line
		position.updated++
	}
}

// --- MCP2515 CAN -----------------------------------------------------------

func canSelect(on bool) {
	if on {
		canCS.Low()
	} else {
		canCS.High()
	}
}

func canTransfer(b byte) byte {
	v, _ := machine.SPI0.Transfer(b)
	return v
}

func canWrite(addr byte, data ...byte) {
	canSelect(true)
	canTransfer(0x02)
	canTransfer(addr)
	for _, b := range data {
		canTransfer(b)
	}
	canSelect(false)
}

func canRead(addr byte, n int) []byte {
	out := make([]byte, n)
	canSelect(true)
	canTransfer(0x03)
	canTransfer(addr)
	for i := range out {
		out[i] = canTransfer(0)
	}
	canSelect(false)
	return out
}

func canReset() {
	canSelect(true)
	canTransfer(0xC0)
	canSelect(false)
	time.Sleep(10 * time.Millisecond)
}

func canInit() {
	canINT.Configure(machine.PinConfig{Mode: machine.PinInputPullup})
	// SPI0 is shared with the LCD; a separate chip select selects the MCP2515.
	canCS.Configure(machine.PinConfig{Mode: machine.PinOutput})
	canCS.High()
	canReset()
	canWrite(0x28, 0x03) // CNF3
	canWrite(0x29, 0x90) // CNF2
	canWrite(0x2A, 0x02) // CNF1
	canWrite(0x2B, 0x03) // CANINTE RX0/RX1
	canWrite(0x2C, 0x00) // clear interrupts
	canWrite(0x0F, 0x00) // normal mode
}

func canExchange() bool {
	// TXB0: standard ID 0x123, one data byte 0x41.
	canWrite(0x31, 0x24, 0x60, 0x00, 0x00, 0x01, 0x41)
	canWrite(0x30, 0x08) // TXREQ
	canSelect(true)
	canTransfer(0x81) // RTS TXB0
	canSelect(false)
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if canRead(0x2C, 1)[0]&0x01 != 0 { // RX0IF
			rx := canRead(0x61, 6)
			canID = uint32(rx[0])<<3 | uint32(rx[1])>>5
			canData = rx[5]
			return true
		}
	}
	return false
}

// --- LCD -------------------------------------------------------------------

func drawStatic(d *st7789.Device) {
	_ = d.FillRectangle(0, 0, 320, 240, bg)
	_ = d.FillRectangle(0, 0, 320, 3, accent)
	tinyfont.WriteLine(d, &freesans.Bold18pt7b, 8, 27, "SENSOR LAB", white)

	_ = d.FillRectangle(6, 38, 155, 178, panel)
	_ = d.FillRectangle(167, 38, 147, 104, panel2)
	_ = d.FillRectangle(167, 148, 147, 68, panel)
	tinyfont.WriteLine(d, &freesans.Regular9pt7b, 12, 54, "1-WIRE", accent)
	tinyfont.WriteLine(d, &freesans.Regular9pt7b, 12, 116, "DHT22", accent)
	tinyfont.WriteLine(d, &freesans.Regular9pt7b, 12, 158, "HC-SR04", accent)
	tinyfont.WriteLine(d, &freesans.Regular9pt7b, 173, 54, "GPS", accent)
	tinyfont.WriteLine(d, &freesans.Regular9pt7b, 173, 164, "CAN", accent)
}

func row(d *st7789.Device, x, y, w int16, text string, c color.RGBA, bgc color.RGBA) {
	_ = d.FillRectangle(x, y-14, w, 18, bgc)
	tinyfont.WriteLine(d, &freesans.Regular9pt7b, x, y, text, c)
}

func drawOneWire(d *st7789.Device) {
	if oneOK == drawnOneOK && oneTemp == drawnOneTemp {
		return
	}
	drawnOneOK, drawnOneTemp = oneOK, oneTemp
	_ = d.FillRectangle(12, 62, 142, 36, panel)
	if oneOK {
		tinyfont.WriteLine(d, &freesans.Bold24pt7b, 12, 94, f2(oneTemp), white)
		tinyfont.WriteLine(d, &freesans.Regular9pt7b, 132, 92, "C", accent)
	} else {
		tinyfont.WriteLine(d, &freesans.Regular9pt7b, 12, 88, "n/a", amber)
	}
}

func drawDHT(d *st7789.Device) {
	if dhtOK == drawnDhtOK && dhtHum == drawnDhtHum && dhtTemp == drawnDhtTemp {
		return
	}
	drawnDhtOK, drawnDhtHum, drawnDhtTemp = dhtOK, dhtHum, dhtTemp
	if dhtOK {
		row(d, 12, 140, 142, fmt.Sprintf("%s %%  %s C", f1(dhtHum), f1(dhtTemp)), white, panel)
	} else {
		row(d, 12, 140, 142, "n/a", amber, panel)
	}
}

func drawDistance(d *st7789.Device) {
	if echoOK == drawnEchoOK && distance == drawnDistance {
		return
	}
	drawnEchoOK, drawnDistance = echoOK, distance
	_ = d.FillRectangle(12, 166, 142, 38, panel)
	if echoOK {
		tinyfont.WriteLine(d, &freesans.Bold18pt7b, 12, 194, fmt.Sprintf("%d", distance), white)
		tinyfont.WriteLine(d, &freesans.Regular9pt7b, 100, 192, "cm", accent)
	} else {
		tinyfont.WriteLine(d, &freesans.Regular9pt7b, 12, 188, "n/a", amber)
	}
}

func drawGPS(d *st7789.Device) {
	if position.ok == drawnGPSOK && position.lat == drawnGPSLat &&
		position.lon == drawnGPSLon && position.sats == drawnGPSSats {
		return
	}
	drawnGPSOK, drawnGPSLat, drawnGPSLon, drawnGPSSats =
		position.ok, position.lat, position.lon, position.sats
	row(d, 240, 54, 70, "", panel2, panel2)
	if position.ok {
		tinyfont.WriteLine(d, &freesans.Regular9pt7b, 245, 54,
			fmt.Sprintf("%d SAT", position.sats), green)
		row(d, 173, 78, 140, fmt.Sprintf("LAT %.4f", position.lat), white, panel2)
		row(d, 173, 96, 140, fmt.Sprintf("LON %.4f", position.lon), white, panel2)
		row(d, 173, 114, 140, "FIX  GGA/RMC", green, panel2)
	} else {
		tinyfont.WriteLine(d, &freesans.Regular9pt7b, 245, 54, "NO FIX", amber)
		row(d, 173, 78, 140, "waiting for", muted, panel2)
		row(d, 173, 96, 140, "NMEA sentences", muted, panel2)
		row(d, 173, 114, 140, "", muted, panel2)
	}
}

func drawCAN(d *st7789.Device) {
	if canOK == drawnCANOK && canTried == drawnCANTried &&
		canID == drawnCANID && canData == drawnCANData {
		return
	}
	drawnCANOK, drawnCANTried, drawnCANID, drawnCANData =
		canOK, canTried, canID, canData
	if canOK {
		row(d, 173, 190, 135, fmt.Sprintf("0x%03X -> 0x%03X", 0x123, canID), white, panel)
		row(d, 173, 208, 135, fmt.Sprintf("data 0x%02X", canData), green, panel)
	} else if canTried {
		row(d, 173, 190, 135, "no peer reply", amber, panel)
		row(d, 173, 208, 135, "classic CAN 2.0B", muted, panel)
	} else {
		row(d, 173, 190, 135, "initialising", muted, panel)
		row(d, 173, 208, 135, "", muted, panel)
	}
}

func drawFoot(d *st7789.Device, tick int) {
	_ = d.FillRectangle(0, 236, 320, 4, panel)
	if tick%2 == 0 {
		_ = d.FillRectangle(0, 236, 320, 4, accent)
	}
}

func main() {
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.GPIO0, RX: machine.GPIO1})
	logf("lab boot")

	// ITM stimulus port 0: the Trace tab and the runner's -itm flag read it.
	*unsafePointer(itmTER) = 1
	*unsafePointer(itmTCR) = 1
	itm("lab: sensor dashboard online\n")

	gps.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.GPIO8, RX: machine.GPIO9})

	machine.SPI0.Configure(machine.SPIConfig{Frequency: 40 * machine.MHz, Mode: 0,
		SCK: machine.GPIO18, SDO: machine.GPIO19, SDI: machine.GPIO4})
	display := st7789.New(machine.SPI0, lcdRST, lcdDC, lcdCS, lcdBL)
	display.Configure(st7789.Config{Width: 240, Height: 320, Rotation: drivers.Rotation90})
	lcdBL.Configure(machine.PinConfig{Mode: machine.PinOutput})
	lcdBL.High()

	oneWirePin.Configure(machine.PinConfig{Mode: machine.PinInputPullup})
	dhtPin.Configure(machine.PinConfig{Mode: machine.PinInputPullup})
	trigPin.Configure(machine.PinConfig{Mode: machine.PinOutput})
	echoPin.Configure(machine.PinConfig{Mode: machine.PinInputPullup})
	trigPin.Low()
	canInit()

	drawStatic(&display)
	// Force the first value draw even when a sensor is absent.
	drawnOneTemp, drawnDhtHum, drawnDhtTemp = -1<<30, -1, -1<<30
	drawnDistance, drawnGPSSats = -1, -1
	drawnGPSLat, drawnGPSLon = 999, 999
	drawnCANID, drawnCANData = ^uint32(0), 0xFF
	pumpGPS()

	dhtOK = readDHT()
	if dhtOK {
		logf("lab dht=%s%% %s C", f1(dhtHum), f1(dhtTemp))
	}
	drawDHT(&display)

	oneOK = readOneWire()
	if oneOK {
		logf("lab onewire=%s C", f2(oneTemp))
	}
	drawOneWire(&display)

	echoOK = readDistance()
	if echoOK {
		logf("lab distance=%d cm", distance)
	}
	drawDistance(&display)

	canTried = true
	canOK = canExchange()
	drawCAN(&display)
	if canOK {
		logf("lab can tx=123 rx=%03x data=%02x", canID, canData)
	}
	drawGPS(&display)

	lastGPS := time.Now()
	lastDHT := time.Now()
	lastOne := time.Now()
	lastCAN := time.Now()
	lastLog := time.Now()
	tick := 0
	for {
		now := time.Now()
		pumpGPS()
		if now.Sub(lastGPS) >= time.Second {
			lastGPS = now
			drawGPS(&display)
		}
		if now.Sub(lastDHT) >= 2*time.Second {
			lastDHT = now
			dhtOK = readDHT()
			drawDHT(&display)
		}
		if now.Sub(lastOne) >= 3*time.Second {
			lastOne = now
			oneOK = readOneWire()
			drawOneWire(&display)
		}
		if now.Sub(lastCAN) >= 2*time.Second {
			lastCAN = now
			canOK = canExchange()
			drawCAN(&display)
		}
		echoOK = readDistance()
		drawDistance(&display)
		drawFoot(&display, tick)
		if now.Sub(lastLog) >= time.Second {
			lastLog = now
			tick++
			drawCount++
			logf("lab t=%d ow=%s dht=%s dist=%d gps=%v can=%v",
				drawCount, f2(oneTemp), f1(dhtTemp), distance, position.ok, canOK)
			itm(fmt.Sprintf("lab: itm tick %d\n", drawCount))
		}
		time.Sleep(100 * time.Millisecond)
	}
}
