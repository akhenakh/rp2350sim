package devices

// DigitalMeasure is the timing of one digital signal in a pin trace. It uses
// the same edge-to-edge method as tools/pincheck: frequency and duty are
// measured between the first and last rising edge, so a partial first/last
// period does not skew the result.
type DigitalMeasure struct {
	// Frequency in Hz, valid with at least two rising edges.
	Frequency float64
	// Period in seconds.
	Period float64
	// Duty is the percentage of time high between the first and last rising
	// edge.
	Duty float64
	// Edges is the number of rising edges in the window.
	Edges int
	// High is the fraction of the whole window spent above the logic
	// threshold, useful for a static level.
	High float64
	// Valid reports whether Frequency/Period/Duty were measured.
	Valid bool
}

// MeasureDigital measures a channel of a pin trace. Interval is the trace's
// sampling interval in 150 MHz cycles.
func MeasureDigital(samples []TraceSample, ch int, interval uint64) DigitalMeasure {
	var m DigitalMeasure
	if len(samples) < 2 || interval == 0 || ch < 0 || ch >= MaxTraceChannels {
		return m
	}
	var rising []int
	prev := samples[0].Bit(ch)
	high := 0
	if prev {
		high++
	}
	for i := 1; i < len(samples); i++ {
		bit := samples[i].Bit(ch)
		if bit {
			high++
		}
		if !prev && bit {
			rising = append(rising, i)
		}
		prev = bit
	}
	m.Edges = len(rising)
	m.High = float64(high) / float64(len(samples))
	if len(rising) < 2 {
		return m
	}
	first, last := rising[0], rising[len(rising)-1]
	periods := len(rising) - 1
	// Sample timestamps are virtual cycles at 150 MHz.
	seconds := float64(samples[last].T-samples[first].T) / 150e6
	m.Period = seconds / float64(periods)
	if m.Period > 0 {
		m.Frequency = 1 / m.Period
	}
	high = 0
	for i := first; i < last; i++ {
		if samples[i].Bit(ch) {
			high++
		}
	}
	m.Duty = 100 * float64(high) / float64(last-first)
	m.Valid = true
	return m
}
