// pincheck measures a GPIO from a rp2350sim logic-analyzer trace and asserts
// simple signal properties (edges, frequency, duty cycle). It is used by
// tools/smoke.sh so PIO/PWM/GPIO firmware is checked on the wire, not only on
// the console.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

type samples struct {
	rate float64   // sample rate in Hz
	t    []float64 // microseconds
	bit  []bool
}

func parsePin(s string) (string, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(s), "GP"))
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 47 {
		return "", fmt.Errorf("invalid pin %q", s)
	}
	return "GP" + strconv.Itoa(n), nil
}

func load(path, pin string) (*samples, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	out := &samples{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	column := -1
	inTable := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "sample_rate_hz:"); ok {
			out.rate, _ = strconv.ParseFloat(strings.TrimSpace(rest), 64)
			continue
		}
		if strings.HasPrefix(line, "# time_us") {
			fields := strings.Fields(line)
			for i, name := range fields {
				if name == pin {
					// Data rows have time_us first, then one field per
					// channel; the header has the comment and time_us first.
					column = i - 1
				}
			}
			if column < 0 {
				return nil, fmt.Errorf("pin %s is not in the trace header", pin)
			}
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		if line == "" {
			break // end of the samples table; decoded sections follow
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) <= column {
			return nil, fmt.Errorf("short sample line %q", line)
		}
		us, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return nil, fmt.Errorf("bad time %q", fields[0])
		}
		bit := fields[column]
		if bit != "0" && bit != "1" {
			return nil, fmt.Errorf("bad level %q", bit)
		}
		out.t = append(out.t, us)
		out.bit = append(out.bit, bit == "1")
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out.t) == 0 {
		return nil, fmt.Errorf("no samples for %s", pin)
	}
	return out, nil
}

type measurement struct {
	transitions int
	rising      int
	freq        float64
	duty        float64
	window      float64 // microseconds
}

func measure(s *samples) measurement {
	m := measurement{window: s.t[len(s.t)-1] - s.t[0]}
	var rise []int
	for i, v := range s.bit {
		if i > 0 && v != s.bit[i-1] {
			m.transitions++
			if v {
				m.rising++
				rise = append(rise, i)
			}
		}
	}
	// Measure the duty over complete periods (first to last rising edge) so a
	// partial window at either end does not skew it.
	start, end := 0, len(s.t)
	if len(rise) >= 2 {
		start, end = rise[0], rise[len(rise)-1]
	}
	var high, total float64
	for i := start; i < end && i+1 < len(s.t); i++ {
		dt := s.t[i+1] - s.t[i]
		total += dt
		if s.bit[i] {
			high += dt
		}
	}
	if total > 0 {
		m.duty = high / total * 100
	}
	if len(rise) >= 2 {
		first, last := s.t[rise[0]], s.t[rise[len(rise)-1]]
		m.freq = float64(len(rise)-1) / ((last - first) / 1e6)
	}
	return m
}

func main() {
	pin := flag.String("pin", "", "GPIO to measure (GP25 or 25)")
	minEdges := flag.Int("min-edges", -1, "minimum number of transitions")
	maxEdges := flag.Int("max-edges", -1, "maximum number of transitions")
	freq := flag.Float64("freq", 0, "expected frequency in Hz (0 to skip)")
	freqTol := flag.Float64("freq-tol", 5, "frequency tolerance in percent")
	duty := flag.Float64("duty", -1, "expected duty cycle in percent (-1 to skip)")
	dutyTol := flag.Float64("duty-tol", 5, "duty tolerance in percentage points")
	flag.Parse()
	if flag.NArg() != 1 || *pin == "" {
		fmt.Fprintln(os.Stderr, "usage: pincheck -pin GP25 [-freq HZ] [-duty PCT] [-min-edges N] trace.txt")
		os.Exit(2)
	}
	name, err := parsePin(*pin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pincheck:", err)
		os.Exit(2)
	}
	s, err := load(flag.Arg(0), name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pincheck:", err)
		os.Exit(1)
	}
	m := measure(s)
	fmt.Printf("pincheck: %s %d samples, %.3f ms, %d edges (%d rising), %.3f Hz, %.2f%% duty\n",
		name, len(s.t), m.window/1000, m.transitions, m.rising, m.freq, m.duty)

	fail := false
	if *minEdges >= 0 && m.transitions < *minEdges {
		fmt.Fprintf(os.Stderr, "pincheck: %d edges, want at least %d\n", m.transitions, *minEdges)
		fail = true
	}
	if *maxEdges >= 0 && m.transitions > *maxEdges {
		fmt.Fprintf(os.Stderr, "pincheck: %d edges, want at most %d\n", m.transitions, *maxEdges)
		fail = true
	}
	if *freq > 0 {
		tol := *freq * *freqTol / 100
		if math.Abs(m.freq-*freq) > tol {
			fmt.Fprintf(os.Stderr, "pincheck: %.3f Hz, want %.3f Hz +/- %.1f%%\n", m.freq, *freq, *freqTol)
			fail = true
		}
	}
	if *duty >= 0 {
		if math.Abs(m.duty-*duty) > *dutyTol {
			fmt.Fprintf(os.Stderr, "pincheck: %.2f%% duty, want %.2f%% +/- %.1f\n", m.duty, *duty, *dutyTol)
			fail = true
		}
	}
	if fail {
		os.Exit(1)
	}
}
