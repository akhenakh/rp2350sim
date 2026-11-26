// pluginlcd is the device-plugin dashboard for the RP2350-Touch-LCD-2.
//
// A Lua plugin at I2C 0x76 models an environment sensor; a compiled WASM
// plugin on UART1 models an air-quality sensor. The firmware talks to both
// through ordinary machine.I2C0 / machine.UART1 calls and paints the readings
// on the ST7789, so the demo exercises the same code it would on a real board.
package main

import (
	"fmt"
	"image/color"
	"machine"
	"strconv"
	"strings"
	"time"

	"tinygo.org/x/drivers"
	"tinygo.org/x/drivers/st7789"
	"tinygo.org/x/tinyfont"
	"tinygo.org/x/tinyfont/freesans"
)

const (
	envAddr = 0x76
	lcdRST  = machine.GPIO20
	lcdDC   = machine.GPIO16
	lcdCS   = machine.GPIO17
	lcdBL   = machine.GPIO15
	uartTX  = machine.GPIO8
	uartRX  = machine.GPIO9
)

var (
	uart   = machine.UART0
	uartAQ = machine.UART1
	i2c    = machine.I2C0

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

type envReading struct {
	temp, hum, press int
	ok               bool
}

type aqReading struct {
	pm, co2, temp, hum int
	ok                 bool
}

var (
	env      envReading
	aq       aqReading
	aqLine   string
	airDrawn bool
	envDrawn bool
	history  [32]int
	histHead int
)

func logf(format string, args ...any) {
	uart.Write([]byte(fmt.Sprintf(format, args...)))
	uart.Write([]byte("\r\n"))
}

func readEnv() {
	var b [8]byte
	if err := i2c.Tx(envAddr, []byte{0x00}, b[:]); err != nil {
		env.ok = false
		return
	}
	env.temp = int(int16(uint16(b[1])<<8 | uint16(b[2])))
	env.hum = int(uint16(b[3])<<8 | uint16(b[4]))
	env.press = int(uint16(b[5])<<8 | uint16(b[6]))
	env.ok = b[0] == 0x5A
}

func parseAQ(line string) {
	parts := strings.Split(line, ",")
	if len(parts) != 5 || parts[0] != "AQ" {
		return
	}
	pm, e1 := strconv.Atoi(parts[1])
	co2, e2 := strconv.Atoi(parts[2])
	temp, e3 := strconv.Atoi(parts[3])
	hum, e4 := strconv.Atoi(parts[4])
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return
	}
	aq = aqReading{pm: pm, co2: co2, temp: temp, hum: hum, ok: true}
}

func pumpAQ() {
	for uartAQ.Buffered() > 0 {
		b, err := uartAQ.ReadByte()
		if err != nil {
			return
		}
		switch b {
		case '\n':
			parseAQ(strings.TrimSpace(aqLine))
			aqLine = ""
		case '\r':
		default:
			if len(aqLine) < 80 {
				aqLine += string(b)
			}
		}
	}
}

func f2(v int) string {
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}

func drawStatic(display *st7789.Device) {
	_ = display.FillRectangle(0, 0, 320, 240, bg)
	_ = display.FillRectangle(0, 0, 320, 4, accent)
	tinyfont.WriteLine(display, &freesans.Bold18pt7b, 10, 28, "PLUGIN LAB", white)
	tinyfont.WriteLine(display, &freesans.Regular9pt7b, 270, 26, "MOCK", muted)

	_ = display.FillRectangle(8, 40, 188, 120, panel)
	tinyfont.WriteLine(display, &freesans.Regular9pt7b, 16, 58, "I2C 0x76  LUA", accent)

	_ = display.FillRectangle(204, 40, 108, 120, panel2)
	tinyfont.WriteLine(display, &freesans.Regular9pt7b, 212, 52, "UART1", accent)
	tinyfont.WriteLine(display, &freesans.Regular9pt7b, 212, 66, "WASM", muted)

	_ = display.FillRectangle(8, 164, 304, 68, panel)
	tinyfont.WriteLine(display, &freesans.Regular9pt7b, 16, 180, "PM2.5 HISTORY", muted)
}

func drawValues(display *st7789.Device) {
	// Environment sensor values are painted once; only the history strip
	// animates. This keeps the SPI traffic small and the readings readable.
	if !envDrawn {
		_ = display.FillRectangle(16, 68, 170, 82, panel)
		if env.ok {
			tinyfont.WriteLine(display, &freesans.Bold24pt7b, 16, 96, f2(env.temp), white)
			tinyfont.WriteLine(display, &freesans.Regular9pt7b, 126, 96, "C", accent)
			tinyfont.WriteLine(display, &freesans.Regular9pt7b, 16, 118,
				fmt.Sprintf("H %s %%", f2(env.hum)), white)
			tinyfont.WriteLine(display, &freesans.Regular9pt7b, 16, 138,
				fmt.Sprintf("P %s hPa", f2(env.press)), white)
			envDrawn = true
		} else {
			tinyfont.WriteLine(display, &freesans.Regular9pt7b, 16, 100, "waiting", amber)
		}
	}

	// Air-quality values are painted once, when the first serial frame
	// arrives. Redrawing them on every tick would only add SPI tearing to a
	// panel whose whole purpose is to be readable.
	if !airDrawn {
		_ = display.FillRectangle(212, 76, 96, 72, panel2)
		if aq.ok {
			tinyfont.WriteLine(display, &freesans.Bold24pt7b, 212, 108,
				fmt.Sprintf("%d", aq.pm), white)
			tinyfont.WriteLine(display, &freesans.Regular9pt7b, 266, 108, "PM2.5", muted)
			tinyfont.WriteLine(display, &freesans.Regular9pt7b, 212, 126,
				fmt.Sprintf("CO2 %d", aq.co2), white)
			bar := aq.pm
			if bar > 60 {
				bar = 60
			}
			_ = display.FillRectangle(212, 144, 92, 6, panel2)
			_ = display.FillRectangle(212, 144, int16(bar*92/60), 6, green)
			airDrawn = true
		} else {
			tinyfont.WriteLine(display, &freesans.Regular9pt7b, 212, 100, "waiting", amber)
		}
	}

	// PM history strip.
	_ = display.FillRectangle(16, 184, 296, 40, panel)
	for i := 0; i < len(history); i++ {
		idx := (histHead + i) % len(history)
		v := history[idx]
		if v > 60 {
			v = 60
		}
		h := int16(v * 30 / 60)
		if h < 2 {
			h = 2
		}
		c := green
		if v > 35 {
			c = amber
		}
		if v > 50 {
			c = red
		}
		_ = display.FillRectangle(int16(16+i*9), int16(220-h), 7, h, c)
	}
}

func main() {
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.GPIO0, RX: machine.GPIO1})
	logf("pluginlcd boot")

	i2c.Configure(machine.I2CConfig{Frequency: 400e3, SDA: machine.GPIO12, SCL: machine.GPIO13})
	uartAQ.Configure(machine.UARTConfig{BaudRate: 115200, TX: uartTX, RX: uartRX})

	machine.SPI0.Configure(machine.SPIConfig{Frequency: 40 * machine.MHz, Mode: 0})
	display := st7789.New(machine.SPI0, lcdRST, lcdDC, lcdCS, lcdBL)
	display.Configure(st7789.Config{Width: 240, Height: 320, Rotation: drivers.Rotation90})
	lcdBL.Configure(machine.PinConfig{Mode: machine.PinOutput})
	lcdBL.High()

	readEnv()
	uartAQ.Write([]byte{'I'})
	uartAQ.Write([]byte{'R'})
	for i := range history {
		history[i] = 12
	}
	drawStatic(&display)
	logf("pluginlcd lcd ready i2c=%v", env.ok)

	lastRead := time.Now()
	lastReq := time.Now()
	lastDraw := time.Now()
	lastLog := time.Now()
	for {
		now := time.Now()
		if now.Sub(lastReq) >= 250*time.Millisecond {
			lastReq = now
			uartAQ.Write([]byte{'R'})
		}
		pumpAQ()
		if now.Sub(lastRead) >= 200*time.Millisecond {
			lastRead = now
			readEnv()
		}
		if now.Sub(lastDraw) >= 500*time.Millisecond {
			lastDraw = now
			if aq.ok {
				history[histHead] = aq.pm
				histHead = (histHead + 1) % len(history)
			}
			drawValues(&display)
		}
		if now.Sub(lastLog) >= time.Second {
			lastLog = now
			logf("pluginlcd t=%d h=%d p=%d pm=%d co2=%d",
				env.temp, env.hum, env.press, aq.pm, aq.co2)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
