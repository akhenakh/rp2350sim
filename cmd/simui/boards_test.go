package main

import (
	"os"
	"testing"
)

// TestTouchPointRotation checks the LCD click mapping for the Touch-LCD-2's
// 90-degree rotation: the displayed 320x240 frame runs along the panel's
// native Y axis, and MX mirrors native X.
func TestTouchPointRotation(t *testing.T) {
	b := &BoardDef{
		LCD:   &LCDDevice{Width: 240, Height: 320, Rotation: 90},
		Touch: &TouchDevice{Width: 240, Height: 320},
	}
	cases := []struct {
		fx, fy float32
		x, y   int
	}{
		{0, 0, 239, 0},
		{1, 1, 0, 319},
		{0.5, 0.5, 119, 160},
	}
	for _, c := range cases {
		x, y := touchPoint(b, c.fx, c.fy, 320, 240)
		if x != c.x || y != c.y {
			t.Errorf("touchPoint(%v,%v) = %d,%d, want %d,%d", c.fx, c.fy, x, y, c.x, c.y)
		}
	}
}

// TestBoardsIncludeTouchLCD2 guards the embedded board list and the GEEK card
// bus (the LCD is SPI1, the card is SPI0).
func TestBoardsIncludeTouchLCD2(t *testing.T) {
	boards, err := LoadBoards()
	if err != nil {
		t.Fatal(err)
	}
	var touch, geek *BoardDef
	for i := range boards {
		switch boards[i].ID {
		case "touch-lcd2":
			touch = &boards[i]
		case "geek":
			geek = &boards[i]
		}
	}
	if touch == nil {
		t.Fatal("touch-lcd2 board is missing")
	}
	if touch.LCD == nil || touch.LCD.Rotation != 90 || touch.LCD.BacklightPin() != 15 {
		t.Fatalf("touch-lcd2 LCD = %+v", touch.LCD)
	}
	if touch.Touch == nil || touch.Touch.Addr != 0x15 || touch.Touch.RSTPin() != 20 {
		t.Fatalf("touch-lcd2 touch = %+v", touch.Touch)
	}
	if touch.IMU == nil || touch.IMU.Addr != 0x6b {
		t.Fatalf("touch-lcd2 imu = %+v", touch.IMU)
	}
	if touch.Battery == nil || touch.Battery.Divider != 3 {
		t.Fatalf("touch-lcd2 battery = %+v", touch.Battery)
	}
	if touch.SD == nil || touch.SD.SPI != 1 || touch.SD.CS != 25 {
		t.Fatalf("touch-lcd2 sd = %+v", touch.SD)
	}
	if geek == nil || geek.SD == nil || geek.SD.SPI != 0 || geek.SD.CS != 23 {
		t.Fatalf("geek sd = %+v", geek)
	}
}

// writeBoards writes a temporary board file and points LoadBoards at it.
func writeBoards(t *testing.T, yaml string) {
	t.Helper()
	path := t.TempDir() + "/boards.yaml"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RP2350SIM_BOARDS", path)
}

// TestBoardsAllowSharedSPI checks that an LCD and an Ethernet controller may
// share a controller when they have different chip selects.
func TestBoardsAllowSharedSPI(t *testing.T) {
	writeBoards(t, `boards:
  - id: shared
    lcd: { spi: 0, cs: 17, dc: 16, rst: 20, width: 240, height: 320 }
    ethernet: { kind: w5500, spi: 0, cs: 20 }
`)
	if _, err := LoadBoards(); err != nil {
		t.Fatalf("shared bus rejected: %v", err)
	}
}

// TestBoardsRejectSameChipSelect checks the real conflict: one CS on one bus.
func TestBoardsRejectSameChipSelect(t *testing.T) {
	writeBoards(t, `boards:
  - id: clash
    lcd: { spi: 0, cs: 17, dc: 16, rst: 20, width: 240, height: 320 }
    ethernet: { kind: w5500, spi: 0, cs: 17 }
`)
	if _, err := LoadBoards(); err == nil {
		t.Fatal("same CS on one bus was accepted")
	}
}

func TestBoardsIncludeBus(t *testing.T) {
	boards, err := LoadBoards()
	if err != nil {
		t.Fatal(err)
	}
	var bus *BoardDef
	for i := range boards {
		if boards[i].ID == "pico2-bus" {
			bus = &boards[i]
		}
	}
	if bus == nil {
		t.Fatal("pico2-bus board is missing")
	}
	if len(bus.I2CTCP) != 1 || bus.I2CTCP[0].Bus != 0 ||
		len(bus.I2CTCP[0].Addrs) != 1 || bus.I2CTCP[0].Addrs[0] != 0x50 {
		t.Fatalf("pico2-bus i2c_tcp = %+v", bus.I2CTCP)
	}
	if len(bus.SPITCP) != 1 || bus.SPITCP[0].Bus != 0 || bus.SPITCP[0].CS != 17 {
		t.Fatalf("pico2-bus spi_tcp = %+v", bus.SPITCP)
	}
}

func TestBoardsRejectBusConflicts(t *testing.T) {
	writeBoards(t, `
boards:
  - id: dup
    touch: { bus: 0, addr: 0x50, int: 1, width: 240, height: 320 }
    i2c_tcp:
      - { bus: 0, addrs: [0x50] }
`)
	if _, err := LoadBoards(); err == nil {
		t.Fatal("duplicate I2C address was accepted")
	}
	writeBoards(t, `
boards:
  - id: dup
    lcd: { spi: 0, cs: 17, dc: 16, rst: 20, width: 240, height: 320 }
    spi_tcp:
      - { bus: 0, cs: 17 }
`)
	if _, err := LoadBoards(); err == nil {
		t.Fatal("duplicate SPI CS was accepted")
	}
}
