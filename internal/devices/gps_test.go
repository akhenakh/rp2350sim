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
		SpeedKnots: 3.2, Course: 270, Satellites: 9, Rate: time.Second,
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
