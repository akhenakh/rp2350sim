package devices

import (
	"strings"
	"testing"
	"time"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func TestParseNMEA(t *testing.T) {
	s, err := ParseNMEA("$GPGGA,123519,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,*47")
	if err != nil {
		t.Fatal(err)
	}
	if s.Kind != "GGA" || !s.Fix || s.Satellites != 8 || s.Altitude != 545.4 {
		t.Fatalf("GGA = %+v", s)
	}
	if s.Lat < 48.117 || s.Lat > 48.118 || s.Lon < 11.516 || s.Lon > 11.517 {
		t.Fatalf("position = %f,%f", s.Lat, s.Lon)
	}
	if _, err := ParseNMEA("$GPGGA,,,,*00"); err == nil {
		t.Fatal("bad checksum accepted")
	}
}

func TestGPSEmits(t *testing.T) {
	c := rp2350.New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	c.Write32(0x4007802c, 1<<4) // UART1 FIFO enable
	NewGPS(c, 1, GPSConfig{
		Lat: 45.5017, Lon: -73.5673, Altitude: 82,
		SpeedMPS: 3.2, Course: 270, Satellites: 9, Rate: time.Second,
	})
	c.Run(1_500_000 * uint64(rp2350.CyclesPerMicrosecond))
	var b strings.Builder
	for c.Read32(0x40078018)&(1<<4) == 0 {
		b.WriteByte(byte(c.Read32(0x40078000)))
	}
	if !strings.HasPrefix(b.String(), "$GPGGA,") {
		t.Fatalf("UART data = %q", b.String())
	}
}

func TestNMEAFormatting(t *testing.T) {
	cases := []struct {
		v    float64
		pos  byte
		neg  byte
		wd   int
		want string
	}{
		{45.5017, 'N', 'S', 2, "4530.1020,N"},
		{-73.5673, 'E', 'W', 3, "07334.0380,W"},
		{151.2, 'E', 'W', 3, "15112.0000,E"},
		{5.0, 'N', 'S', 2, "0500.0000,N"},
	}
	for _, c := range cases {
		if got := formatNMEACoord(c.v, c.pos, c.neg, c.wd); got != c.want {
			t.Errorf("formatNMEACoord(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestGPSGoldenSentences(t *testing.T) {
	c := rp2350.New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	c.Write32(0x4007802c, 1<<4)
	NewGPS(c, 1, GPSConfig{
		Lat: 45.5017, Lon: -73.5673, Altitude: 82,
		Course: 270, Satellites: 9,
		Rate: time.Second, FixSet: true, Fix: true,
	})
	c.Run(1_600_000 * uint64(rp2350.CyclesPerMicrosecond))
	var b strings.Builder
	for c.Read32(0x40078018)&(1<<4) == 0 {
		b.WriteByte(byte(c.Read32(0x40078000)))
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\r\n")
	if len(lines) < 2 {
		t.Fatalf("sentences = %q", b.String())
	}
	if !strings.Contains(lines[0], "4530.1020,N,07334.0380,W") {
		t.Fatalf("GGA = %q", lines[0])
	}
	for _, line := range lines {
		if _, err := ParseNMEA(line); err != nil {
			t.Fatalf("ParseNMEA(%q): %v", line, err)
		}
	}
	if !strings.Contains(lines[1], ",A,") {
		t.Fatalf("RMC status = %q", lines[1])
	}
}

func TestGPSMovesAtSpeed(t *testing.T) {
	c := rp2350.New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	c.Write32(0x4007802c, 1<<4)
	g := NewGPS(c, 1, GPSConfig{
		Lat: 45.5017, Lon: -73.5673, Altitude: 82,
		SpeedMPS: 10, Course: 90, Satellites: 9,
		Rate: time.Second, FixSet: true, Fix: true,
	})
	c.Run(1_100_000 * uint64(rp2350.CyclesPerMicrosecond))
	info := g.Info()
	if info.Lon <= -73.5673 {
		t.Fatalf("eastward movement did not move longitude: %f", info.Lon)
	}
	if info.Lat < 45.5016 || info.Lat > 45.5018 {
		t.Fatalf("eastward movement changed latitude: %f", info.Lat)
	}
}

func TestGPSSetFieldSpeedKeepsMoving(t *testing.T) {
	c := rp2350.New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	c.Write32(0x4007802c, 1<<4)
	g := NewGPS(c, 1, GPSConfig{
		Lat: 45.5017, Lon: -73.5673, Altitude: 82,
		Course: 90, Satellites: 9, Rate: time.Second, FixSet: true, Fix: true,
	})
	c.Run(1_100_000 * uint64(rp2350.CyclesPerMicrosecond))
	if err := g.SetField("speed", 10); err != nil {
		t.Fatal(err)
	}
	before := g.Info().Lon
	c.Run(2_100_000 * uint64(rp2350.CyclesPerMicrosecond))
	after := g.Info().Lon
	if after <= before {
		t.Fatalf("speed edit stopped movement: %f -> %f", before, after)
	}
}

func TestGPSNoFix(t *testing.T) {
	c := rp2350.New()
	c.CPU0.Sleeping = true
	c.CPU1.Parked = true
	c.Write32(0x4007802c, 1<<4)
	NewGPS(c, 1, GPSConfig{Lat: 45.5017, Lon: -73.5673, Rate: time.Second, FixSet: true})
	c.Run(1_600_000 * uint64(rp2350.CyclesPerMicrosecond))
	var b strings.Builder
	for c.Read32(0x40078018)&(1<<4) == 0 {
		b.WriteByte(byte(c.Read32(0x40078000)))
	}
	if !strings.Contains(b.String(), ",0,09,") || !strings.Contains(b.String(), ",V,") {
		t.Fatalf("no-fix sentences = %q", b.String())
	}
}
