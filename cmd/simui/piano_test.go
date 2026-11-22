package main

import "testing"

func TestPianoKeyLayout(t *testing.T) {
	const width float32 = 1400 // 14 white keys, 100 px each
	min, max := 60, 83
	layout := buildKeyLayout(width, min, max)

	if got := layout[60-min]; got.X != 0 || got.W != 100 {
		t.Fatalf("C4 layout = %+v, want x=0 w=100", got)
	}
	if got := layout[62-min]; got.X != 100 || got.W != 100 {
		t.Fatalf("D4 layout = %+v, want x=100 w=100", got)
	}
	if got := layout[83-min]; got.X < 1300 || got.W != 100 {
		t.Fatalf("B5 layout = %+v", got)
	}
	// C# sits on the C/D boundary: x = (1 - 0.30) * 100, width 60.
	if got := layout[61-min]; got.X != 70 || !near(got.W, 60, 0.001) {
		t.Fatalf("C#4 layout = %+v, want x=70 w=60", got)
	}

	cases := []struct {
		x    float32
		want int
	}{
		{0, 60},
		{20, 60},
		{75, 61},   // inside both C4 and C#4: the black key wins
		{140, 62},  // inside D4, left of D#4
		{1200, 80}, // G#5
		{1399, 83}, // last white key
	}
	for _, c := range cases {
		if got := keyAt(c.x, layout, min, max); got != c.want {
			t.Errorf("keyAt(%v) = %d, want %d", c.x, got, c.want)
		}
	}
}

func TestPianoKeyLayoutEmpty(t *testing.T) {
	if got := buildKeyLayout(0, 60, 83); len(got) != 24 {
		t.Fatalf("layout length = %d", len(got))
	}
	if got := keyAt(10, buildKeyLayout(0, 60, 83), 60, 83); got != 60 {
		t.Fatalf("keyAt with no width = %d, want 60", got)
	}
}

func near(got, want, eps float32) bool {
	if got > want {
		return got-want <= eps
	}
	return want-got <= eps
}
