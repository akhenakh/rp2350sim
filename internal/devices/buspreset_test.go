package devices

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func i2cWrite(t *testing.T, dev rp2350.I2CDevice, reg byte, data ...byte) {
	t.Helper()
	if tr, ok := dev.(rp2350.I2CTransaction); ok {
		tr.BeginTransaction()
	}
	if wd, ok := dev.(rp2350.I2CWireDevice); ok {
		if !wd.WriteAck(reg) {
			t.Fatalf("register %#x NAKed", reg)
		}
		for _, b := range data {
			if !wd.WriteAck(b) {
				t.Fatalf("data %#x NAKed", b)
			}
		}
		return
	}
	dev.Write(reg)
	for _, b := range data {
		dev.Write(b)
	}
}

func i2cRead(t *testing.T, dev rp2350.I2CDevice, reg byte, n int) []byte {
	t.Helper()
	if tr, ok := dev.(rp2350.I2CTransaction); ok {
		tr.BeginTransaction()
	}
	dev.Write(reg)
	out := make([]byte, n)
	for i := range out {
		out[i] = dev.Read()
	}
	return out
}

func TestBME280Preset(t *testing.T) {
	dev, addr, err := NewI2CPreset("bme280", 0)
	if err != nil {
		t.Fatal(err)
	}
	if addr != 0x76 {
		t.Fatalf("default address = %#x, want 0x76", addr)
	}
	if got := i2cRead(t, dev, 0xD0, 1); got[0] != 0x60 {
		t.Fatalf("chip id = %#x, want 0x60", got[0])
	}
	// Temperature and pressure registers are populated and non-zero.
	data := i2cRead(t, dev, 0xF7, 8)
	if data[0] == 0 && data[1] == 0 && data[2] == 0 {
		t.Fatalf("pressure registers are zero: % x", data[:3])
	}
	if data[3] == 0 && data[4] == 0 && data[5] == 0 {
		t.Fatalf("temperature registers are zero: % x", data[3:6])
	}
	// Second address and reset.
	if _, addr, err := NewI2CPreset("bme280", 0x77); err != nil || addr != 0x77 {
		t.Fatalf("0x77 address: addr=%#x err=%v", addr, err)
	}
	if _, _, err := NewI2CPreset("bme280", 0x50); err == nil {
		t.Fatal("unsupported address was accepted")
	}
	i2cWrite(t, dev, 0xE0, 0xB6)
	if got := i2cRead(t, dev, 0xD0, 1); got[0] != 0x60 {
		t.Fatalf("chip id after reset = %#x", got[0])
	}
	if _, _, err := NewI2CPreset("w25q16", 0); err == nil {
		t.Fatal("SPI preset was accepted as I2C")
	}
}

func TestMPU6050Preset(t *testing.T) {
	dev, addr, err := NewI2CPreset("mpu6050", 0)
	if err != nil {
		t.Fatal(err)
	}
	if addr != 0x68 {
		t.Fatalf("default address = %#x, want 0x68", addr)
	}
	if got := i2cRead(t, dev, 0x75, 1); got[0] != 0x68 {
		t.Fatalf("WHO_AM_I = %#x, want 0x68", got[0])
	}
	// Accel burst: X=0, Y=0, Z=1 g.
	got := i2cRead(t, dev, 0x3B, 6)
	want := []byte{0x00, 0x00, 0x00, 0x00, 0x40, 0x00}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("accel burst = % x, want % x", got, want)
		}
	}
	// Waking the part must not lose WHO_AM_I.
	i2cWrite(t, dev, 0x6B, 0x00)
	if got := i2cRead(t, dev, 0x75, 1); got[0] != 0x68 {
		t.Fatalf("WHO_AM_I after wake = %#x", got[0])
	}
}

func TestSSD1306Preset(t *testing.T) {
	dev, addr, err := NewI2CPreset("ssd1306", 0)
	if err != nil {
		t.Fatal(err)
	}
	if addr != 0x3C {
		t.Fatalf("default address = %#x, want 0x3c", addr)
	}
	oled := dev.(*ssd1306)
	i2cWrite(t, dev, 0x00, 0xAF) // display on
	if !oled.DisplayOn() {
		t.Fatal("display did not turn on")
	}
	// Set the column window to 2..4 and page 1, then write three pixels.
	i2cWrite(t, dev, 0x00, 0x21, 0x02, 0x04)
	i2cWrite(t, dev, 0x00, 0x22, 0x01, 0x01)
	i2cWrite(t, dev, 0x40, 0xAA, 0xBB, 0xCC)
	gram := oled.GRAM()
	if got := gram[128+2 : 128+5]; got[0] != 0xAA || got[1] != 0xBB || got[2] != 0xCC {
		t.Fatalf("GRAM window = % x", got)
	}
	if v, ok := dev.(rp2350.I2CWireDevice).ReadAck(); !ok || v != 0 {
		t.Fatalf("read = %#x, %v", v, ok)
	}
}

func TestW25Q16Preset(t *testing.T) {
	p, err := NewSPIPreset("w25q16")
	if err != nil {
		t.Fatal(err)
	}
	jdec := []byte{0xEF, 0x40, 0x15}
	p.Select()
	for i, want := range jdec {
		if got := p.Transfer(0x9F); got != want {
			t.Fatalf("JEDEC byte %d = %#x, want %#x", i, got, want)
		}
	}
	// Write enable, page program three bytes, read them back.
	p.Select()
	p.Transfer(0x06)
	p.Select()
	for _, b := range []byte{0x02, 0x00, 0x10, 0x00} {
		p.Transfer(b)
	}
	for _, b := range []byte{0x12, 0x34, 0x56} {
		p.Transfer(b)
	}
	p.Select()
	for _, b := range []byte{0x03, 0x00, 0x10, 0x00} {
		p.Transfer(b)
	}
	for i, want := range []byte{0x12, 0x34, 0x56} {
		if got := p.Transfer(0); got != want {
			t.Fatalf("read %d = %#x, want %#x", i, got, want)
		}
	}
	// Erase the 4 KiB sector and confirm the bytes are 0xFF again.
	p.Select()
	for _, b := range []byte{0x20, 0x00, 0x10, 0x00} {
		p.Transfer(b)
	}
	p.Select()
	for _, b := range []byte{0x03, 0x00, 0x10, 0x00} {
		p.Transfer(b)
	}
	if got := p.Transfer(0); got != 0xFF {
		t.Fatalf("erased byte = %#x, want 0xff", got)
	}
}

func TestBusPresetRegistry(t *testing.T) {
	list := BusPresetList()
	if len(list) < 4 {
		t.Fatalf("preset list = %+v", list)
	}
	for _, name := range []string{"bme280", "ssd1306", "mpu6050", "w25q16"} {
		if _, ok := FindBusPreset(name); !ok {
			t.Fatalf("preset %q missing", name)
		}
	}
	if _, ok := FindBusPreset("BME280"); !ok {
		t.Fatal("preset lookup is not case-insensitive")
	}
	if _, ok := FindBusPreset("nope"); ok {
		t.Fatal("unknown preset accepted")
	}
	if _, err := NewSPIPreset("bme280"); err == nil {
		t.Fatal("I2C preset accepted as SPI")
	}
}
