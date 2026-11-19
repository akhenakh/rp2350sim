// Drive the Waveshare RP2350-Touch-LCD-2: ST7789T3 on SPI0, CST816D touch and
// QMI8658 IMU on I2C0, PWM backlight on GP15 and the battery divider on GP28.
// The loop prints deterministic values over UART so the emulator's smoke test
// can assert every on-board device.
package main

import (
	"image/color"
	"machine"
	"time"

	"tinygo.org/x/drivers"
	"tinygo.org/x/drivers/st7789"
)

const (
	lcdRST    = machine.GPIO20
	lcdDC     = machine.GPIO16
	lcdCS     = machine.GPIO17
	lcdBL     = machine.GPIO15
	touchRST  = machine.GPIO20
	touchINT  = machine.GPIO29
	imuAddr   = 0x6b
	touchAddr = 0x15
)

var (
	uart  = machine.UART0
	i2c   = machine.I2C0
	adc   = machine.ADC{Pin: machine.GPIO28}
	dark  = color.RGBA{R: 10, G: 12, B: 18, A: 255}
	green = color.RGBA{R: 40, G: 220, B: 120, A: 255}
	red   = color.RGBA{R: 240, G: 60, B: 60, A: 255}
)

func logf(s string) {
	uart.Write([]byte(s))
	uart.Write([]byte("\r\n"))
}

// touchRead returns the point in the controller's native panel coordinates.
func touchRead() (x, y int, pressed bool) {
	var buf [5]byte
	if err := i2c.Tx(touchAddr, []byte{0x02}, buf[:]); err != nil {
		return 0, 0, false
	}
	if buf[0] == 0 {
		return 0, 0, false
	}
	x = int(buf[1]&0x0F)<<8 | int(buf[2])
	y = int(buf[3]&0x0F)<<8 | int(buf[4])
	return x, y, true
}

func touchInit() {
	touchRST.Configure(machine.PinConfig{Mode: machine.PinOutput})
	touchRST.Low()
	time.Sleep(10 * time.Millisecond)
	touchRST.High()
	time.Sleep(50 * time.Millisecond)
	// Disable auto sleep, point mode, short IRQ pulse, 1 ms scan period.
	_ = i2c.Tx(touchAddr, []byte{0xFE, 0x01}, nil)
	_ = i2c.Tx(touchAddr, []byte{0xFA, 0x41}, nil)
	_ = i2c.Tx(touchAddr, []byte{0xED, 0x01}, nil)
	_ = i2c.Tx(touchAddr, []byte{0xEE, 0x01}, nil)
}

func imuInit() uint8 {
	var id [1]byte
	_ = i2c.Tx(imuAddr, []byte{0x60, 0xB0}, nil) // soft reset
	time.Sleep(10 * time.Millisecond)
	_ = i2c.Tx(imuAddr, []byte{0x00}, id[:])
	// auto-increment, 8 g / 1 kHz, 512 dps / 1 kHz, no low-pass, accel+gyro.
	for _, reg := range [][2]byte{
		{0x02, 0x40}, {0x03, 0x23}, {0x04, 0x53}, {0x05, 0x00},
		{0x06, 0x00}, {0x07, 0x00}, {0x08, 0x03},
	} {
		_ = i2c.Tx(imuAddr, reg[:], nil)
	}
	return id[0]
}

func accelRaw() (int16, int16, int16) {
	var b [6]byte
	if err := i2c.Tx(imuAddr, []byte{0x35}, b[:]); err != nil {
		return 0, 0, 0
	}
	return int16(uint16(b[1])<<8 | uint16(b[0])),
		int16(uint16(b[3])<<8 | uint16(b[2])),
		int16(uint16(b[5])<<8 | uint16(b[4]))
}

func main() {
	uart.Configure(machine.UARTConfig{BaudRate: 115200, TX: machine.UART0_TX_PIN})
	logf("touchlcd boot")

	i2c.Configure(machine.I2CConfig{Frequency: 400e3, SDA: machine.GPIO12, SCL: machine.GPIO13})
	touchInit()
	id := imuInit()
	touchX, touchY, _ := touchRead()

	machine.SPI0.Configure(machine.SPIConfig{Frequency: 40 * machine.MHz, Mode: 0})
	display := st7789.New(machine.SPI0, lcdRST, lcdDC, lcdCS, lcdBL)
	display.Configure(st7789.Config{Width: 240, Height: 320, Rotation: drivers.Rotation90})
	display.FillScreen(dark)
	display.FillRectangle(8, 8, 304, 60, color.RGBA{R: 24, G: 70, B: 60, A: 255})
	display.FillRectangle(8, 80, 304, 4, green)

	// Backlight on. Firmware that wants dimming drives GP15 with PWM; the
	// emulator derives the panel brightness from the compare value.
	lcdBL.Configure(machine.PinConfig{Mode: machine.PinOutput})
	lcdBL.High()

	machine.InitADC()
	_ = adc.Configure(machine.ADCConfig{})

	logf("touchlcd lcd ready")
	logf("touchlcd imu id=" + itoa(int(id)))
	if touchX != 0 || touchY != 0 {
		logf("touchlcd touch residual")
	}

	var lastX, lastY int16 = -1, -1
	var frame int
	for {
		x, y, pressed := touchRead()
		if pressed {
			// The driver's logical frame is rotated 90 degrees.
			lx := int16(y)
			ly := int16(240 - x)
			if lx != lastX || ly != lastY {
				if lastX >= 0 {
					display.FillRectangle(lastX-6, lastY-6, 12, 12, dark)
				}
				display.FillRectangle(lx-6, ly-6, 12, 12, red)
				logf("touchlcd touch x=" + itoa(int(x)) + " y=" + itoa(int(y)))
				lastX, lastY = lx, ly
			}
		} else if lastX >= 0 {
			display.FillRectangle(lastX-6, lastY-6, 12, 12, dark)
			lastX, lastY = -1, -1
			logf("touchlcd release")
		}

		if frame%5 == 0 {
			ax, ay, az := accelRaw()
			raw := adc.Get()
			mv := int(raw) * 3300 / 65535 * 3
			logf("touchlcd imu ax=" + itoa(int(ax)) + " ay=" + itoa(int(ay)) + " az=" + itoa(int(az)))
			logf("touchlcd battery raw=" + itoa(int(raw)) + " mv=" + itoa(mv))
		}
		if frame%20 == 0 {
			logf("touchlcd alive")
		}
		frame++
		time.Sleep(100 * time.Millisecond)
	}
}

// itoa is a tiny decimal formatter so the demo needs no fmt in flash.
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
