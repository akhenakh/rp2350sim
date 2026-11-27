package devices

import (
	"fmt"
	"strconv"
	"strings"
)

// NMEASentence is the parsed subset used by the emulator: position, fix,
// altitude and velocity from GGA/RMC.
type NMEASentence struct {
	Kind       string
	Time       string
	Lat, Lon   float64
	Fix        bool
	Satellites int
	Altitude   float64
	SpeedKnots float64
	Course     float64
}

// ParseNMEA validates the checksum and parses one GGA or RMC sentence.
func ParseNMEA(line string) (NMEASentence, error) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "$") {
		return NMEASentence{}, fmt.Errorf("nmea: missing $")
	}
	star := strings.LastIndexByte(line, '*')
	if star < 0 || star+2 >= len(line) {
		return NMEASentence{}, fmt.Errorf("nmea: missing checksum")
	}
	want, err := strconv.ParseUint(line[star+1:star+3], 16, 8)
	if err != nil {
		return NMEASentence{}, fmt.Errorf("nmea: bad checksum: %w", err)
	}
	body := line[1:star]
	var got byte
	for i := 0; i < len(body); i++ {
		got ^= body[i]
	}
	if got != byte(want) {
		return NMEASentence{}, fmt.Errorf("nmea: checksum %02X, want %02X", got, want)
	}
	f := strings.Split(body, ",")
	s := NMEASentence{}
	switch f[0] {
	case "GPGGA", "GNGGA":
		s.Kind = "GGA"
		if len(f) < 10 {
			return s, fmt.Errorf("nmea: short GGA")
		}
		s.Time = f[1]
		s.Lat, err = parseNMEACoord(f[2], f[3])
		if err != nil {
			return s, err
		}
		s.Lon, err = parseNMEACoord(f[4], f[5])
		if err != nil {
			return s, err
		}
		s.Fix = f[6] != "0" && f[6] != ""
		s.Satellites, _ = strconv.Atoi(f[7])
		s.Altitude, _ = strconv.ParseFloat(f[9], 64)
	case "GPRMC", "GNRMC":
		s.Kind = "RMC"
		if len(f) < 9 {
			return s, fmt.Errorf("nmea: short RMC")
		}
		s.Time = f[1]
		s.Fix = f[2] == "A"
		s.Lat, err = parseNMEACoord(f[3], f[4])
		if err != nil {
			return s, err
		}
		s.Lon, err = parseNMEACoord(f[5], f[6])
		if err != nil {
			return s, err
		}
		s.SpeedKnots, _ = strconv.ParseFloat(f[7], 64)
		s.Course, _ = strconv.ParseFloat(f[8], 64)
	default:
		return s, fmt.Errorf("nmea: unsupported %q", f[0])
	}
	return s, nil
}

func parseNMEACoord(v, hemi string) (float64, error) {
	if v == "" {
		return 0, nil
	}
	x, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("nmea: coordinate %q", v)
	}
	deg := float64(int(x / 100))
	min := x - deg*100
	out := deg + min/60
	switch hemi {
	case "S", "W":
		out = -out
	}
	return out, nil
}

// formatNMEACoord formats decimal degrees as ddmm.mmmm,hemisphere.
func formatNMEACoord(v float64, pos, neg byte, degWidth int) string {
	hemi := pos
	if v < 0 {
		hemi = neg
		v = -v
	}
	deg := int(v)
	min := (v - float64(deg)) * 60
	return fmt.Sprintf("%0*d%07.4f,%c", degWidth, deg, min, hemi)
}
