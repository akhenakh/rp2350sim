package devices

import "testing"

func TestMeasureDigital(t *testing.T) {
	// 1 MHz sampling, 100-sample period, 50% duty => 10 kHz.
	const interval = 150
	var samples []TraceSample
	for i := range 500 {
		var s TraceSample
		s.T = uint64(i) * interval
		if i%100 < 50 {
			s.V[0] = 3.3
		}
		samples = append(samples, s)
	}
	m := MeasureDigital(samples, 0, interval)
	if !m.Valid {
		t.Fatal("no measurement")
	}
	if m.Frequency < 9999 || m.Frequency > 10001 {
		t.Fatalf("frequency = %v, want 10 kHz", m.Frequency)
	}
	if m.Duty < 49 || m.Duty > 51 {
		t.Fatalf("duty = %v, want 50%%", m.Duty)
	}
	if m.Edges != 4 {
		t.Fatalf("edges = %d, want 4", m.Edges)
	}
	if m.Period < 99e-6 || m.Period > 101e-6 {
		t.Fatalf("period = %v, want 100 us", m.Period)
	}
}

func TestMeasureDigitalNoEdges(t *testing.T) {
	samples := []TraceSample{{V: [MaxTraceChannels]float32{3.3}}, {V: [MaxTraceChannels]float32{3.3}}}
	m := MeasureDigital(samples, 0, 150)
	if m.Valid || m.Edges != 0 || m.High != 1 {
		t.Fatalf("measure = %+v, want a static high", m)
	}
}
