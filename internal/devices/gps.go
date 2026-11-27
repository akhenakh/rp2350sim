package devices

import (
	"fmt"
	"time"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// GPSConfig describes the simulated NMEA source.
type GPSConfig struct {
	Lat, Lon   float64
	Altitude   float64
	SpeedKnots float64
	Course     float64
	Satellites int
	Fix        bool
	FixSet     bool
	Rate       time.Duration
}

// GPS emits GGA and RMC sentences on a hardware UART receive FIFO.
type GPS struct {
	c         *rp2350.Chip
	uart      int
	cfg       GPSConfig
	next      uint64
	seconds   uint64
	last      string
	history   []string
	sentences int
}

// GPSInfo is a snapshot for the GPS tab.
type GPSInfo struct {
	UART       int
	Lat, Lon   float64
	Altitude   float64
	SpeedKnots float64
	Course     float64
	Satellites int
	Fix        bool
	Sentences  int
	Last       string
	History    []string
}

// Info returns the current simulated state.
func (g *GPS) Info() GPSInfo {
	return GPSInfo{
		UART: g.uart, Lat: g.cfg.Lat, Lon: g.cfg.Lon,
		Altitude: g.cfg.Altitude, SpeedKnots: g.cfg.SpeedKnots,
		Course: g.cfg.Course, Satellites: g.cfg.Satellites, Fix: g.cfg.Fix,
		Sentences: g.sentences, Last: g.last, History: append([]string(nil), g.history...),
	}
}

// NewGPS attaches a GPS to a UART and starts its periodic emitter.
func NewGPS(c *rp2350.Chip, uart int, cfg GPSConfig) *GPS {
	if cfg.Rate <= 0 {
		cfg.Rate = time.Second
	}
	if cfg.Satellites == 0 {
		cfg.Satellites = 9
	}
	if !cfg.FixSet {
		cfg.Fix = true
	}
	if cfg.Lat == 0 && cfg.Lon == 0 {
		cfg.Lat, cfg.Lon = 45.5017, -73.5673
	}
	if cfg.Altitude == 0 {
		cfg.Altitude = 82
	}
	g := &GPS{c: c, uart: uart, cfg: cfg}
	g.next = c.Cycles + uint64(cfg.Rate/time.Microsecond)*uint64(rp2350.CyclesPerMicrosecond)
	c.AddPeriodicHook(rp2350.CyclesPerMicrosecond*1000, g.tick)
	c.AddResetHook(g.Reset)
	return g
}

// SetConfig replaces the simulated position and motion values. The next
// sentence uses them; the already emitted history is left untouched.
func (g *GPS) SetConfig(lat, lon, altitude, speedKnots, course float64) error {
	if lat < -90 || lat > 90 {
		return fmt.Errorf("latitude must be between -90 and 90")
	}
	if lon < -180 || lon > 180 {
		return fmt.Errorf("longitude must be between -180 and 180")
	}
	if altitude < -1000 || altitude > 10000 {
		return fmt.Errorf("altitude must be between -1000 and 10000 m")
	}
	if speedKnots < 0 || speedKnots > 1000 {
		return fmt.Errorf("speed must be between 0 and 1000 kn")
	}
	if course < 0 || course >= 360 {
		return fmt.Errorf("course must be between 0 and 360")
	}
	g.cfg.Lat, g.cfg.Lon = lat, lon
	g.cfg.Altitude, g.cfg.SpeedKnots, g.cfg.Course = altitude, speedKnots, course
	return nil
}

// TX accepts host bytes; the GPS is output-only.
func (g *GPS) TX(byte) {}

// Reset restarts the sentence schedule.
func (g *GPS) Reset() {
	g.next = g.c.Cycles + uint64(g.cfg.Rate/time.Microsecond)*uint64(rp2350.CyclesPerMicrosecond)
	g.seconds = 0
	g.sentences = 0
	g.last = ""
	g.history = nil
}

func (g *GPS) tick() {
	if g.c.Cycles < g.next {
		return
	}
	g.next = g.c.Cycles + uint64(g.cfg.Rate/time.Microsecond)*uint64(rp2350.CyclesPerMicrosecond)
	g.seconds++
	t := fmt.Sprintf("%02d%02d%02d.00", (g.seconds/3600)%24, (g.seconds/60)%60, g.seconds%60)
	lat := formatNMEACoord(g.cfg.Lat, 'N', 'S', 2)
	lon := formatNMEACoord(g.cfg.Lon, 'E', 'W', 3)
	fix := 0
	if g.cfg.Fix {
		fix = 1
	}
	gga := fmt.Sprintf("GPGGA,%s,%s,%s,%d,%02d,0.9,%.1f,M,0.0,M,,",
		t, lat, lon, fix, g.cfg.Satellites, g.cfg.Altitude)
	rmc := fmt.Sprintf("GPRMC,%s,%s,%s,%s,%.2f,%.1f,010125,,,A",
		t, map[bool]string{true: "A", false: "V"}[g.cfg.Fix], lat, lon,
		g.cfg.SpeedKnots, g.cfg.Course)
	for _, body := range []string{gga, rmc} {
		line := nmeaLine(body)
		g.sentences++
		g.last = line
		g.history = append(g.history, line)
		if len(g.history) > 200 {
			g.history = g.history[len(g.history)-200:]
		}
		g.c.SendUART(g.uart, []byte(line))
	}
}

func nmeaLine(body string) string {
	var cs byte
	for i := 0; i < len(body); i++ {
		cs ^= body[i]
	}
	return fmt.Sprintf("$%s*%02X\r\n", body, cs)
}
