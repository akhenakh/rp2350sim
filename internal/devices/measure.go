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
	// Rise and Fall are the mean 10%–90% and 90%–10% transition times in
	// seconds, measured on the analog samples. They are valid only when the
	// window contains a measurable transition on each edge.
	Rise, Fall           float64
	RiseValid, FallValid bool
	// MinV and MaxV are the analog extremes in the window.
	MinV, MaxV float32
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
	m.measureAnalog(ch, samples)
	return m
}

// measureAnalog computes 10–90% rise/fall times from the voltage samples. A
// threshold crossing is located with the samples either side; if the window
// does not contain both 10% and 90% points (a very fast edge relative to the
// trace rate) that edge is simply not counted.
func (m *DigitalMeasure) measureAnalog(ch int, samples []TraceSample) {
	minV, maxV := samples[0].V[ch], samples[0].V[ch]
	for i := 1; i < len(samples); i++ {
		v := samples[i].V[ch]
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	m.MinV, m.MaxV = minV, maxV
	span := maxV - minV
	if span < 0.05 {
		return
	}
	low := minV + span*0.1
	high := minV + span*0.9
	mid := minV + span*0.5
	var rise, fall float64
	var rises, falls int
	for i := 1; i < len(samples); i++ {
		prev, cur := samples[i-1].V[ch], samples[i].V[ch]
		switch {
		case prev < mid && cur >= mid:
			j := i - 1
			for j > 0 && samples[j].V[ch] > low {
				j--
			}
			k := i
			for k < len(samples)-1 && samples[k].V[ch] < high {
				k++
			}
			if samples[j].V[ch] <= low && samples[k].V[ch] >= high && k > j {
				rise += float64(samples[k].T-samples[j].T) / 150e6
				rises++
			}
		case prev >= mid && cur < mid:
			j := i - 1
			for j > 0 && samples[j].V[ch] < high {
				j--
			}
			k := i
			for k < len(samples)-1 && samples[k].V[ch] > low {
				k++
			}
			if samples[j].V[ch] >= high && samples[k].V[ch] <= low && k > j {
				fall += float64(samples[k].T-samples[j].T) / 150e6
				falls++
			}
		}
	}
	if rises > 0 {
		m.Rise, m.RiseValid = rise/float64(rises), true
	}
	if falls > 0 {
		m.Fall, m.FallValid = fall/float64(falls), true
	}
}
