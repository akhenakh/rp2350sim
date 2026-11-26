package devices

import (
	"math"
	"testing"
)

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

func TestMeasureAnalogRiseFall(t *testing.T) {
	// 1 kHz sine, 1.65 V amplitude around 1.65 V, sampled at 1 MHz. The
	// 10–90% transition time of a sine is about 295 us.
	const interval = 150
	var samples []TraceSample
	for i := range 2000 {
		var s TraceSample
		s.T = uint64(i) * interval
		s.V[0] = float32(1.65 + 1.65*math.Sin(2*math.Pi*1000*float64(i)/1e6))
		samples = append(samples, s)
	}
	m := MeasureDigital(samples, 0, interval)
	if !m.RiseValid || !m.FallValid {
		t.Fatalf("rise/fall not measured: %+v", m)
	}
	for name, got := range map[string]float64{"rise": m.Rise, "fall": m.Fall} {
		if math.Abs(got-295e-6) > 15e-6 {
			t.Fatalf("%s = %v, want ~295us", name, got)
		}
	}
	if m.MinV > 0.05 || m.MaxV < 3.25 {
		t.Fatalf("min/max = %v/%v", m.MinV, m.MaxV)
	}
}
