package main

import "testing"

func TestScopePinColumnsAdaptToWidth(t *testing.T) {
	for _, tc := range []struct {
		width float32
		want  int
	}{
		{64, 1},
		{100, 1},
		{140, 2},
		{200, 3},
		{1100, 16},
	} {
		if got := scopePinColumns(tc.width); got != tc.want {
			t.Errorf("scopePinColumns(%v) = %d, want %d", tc.width, got, tc.want)
		}
	}
}
