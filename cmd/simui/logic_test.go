package main

import (
	"os"
	"strings"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/devices"
)

func TestSaveLogicTraceFile(t *testing.T) {
	oldBoard := selectedBoard
	defer func() { selectedBoard = oldBoard }()
	selectedBoard = &BoardDef{ID: "test-board", Name: "Test Board"}
	samples := []devices.TraceSample{
		{T: 100, V: [devices.MaxTraceChannels]float32{3.3, 0}},
		{T: 175, V: [devices.MaxTraceChannels]float32{0, 3.3}},
	}
	path, err := saveLogicTrace([]int{0, 1}, samples, devices.LogicInterval)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# rp2350sim logic analyzer trace",
		"board: test-board",
		"channels: GP0 GP1",
		"0.500 0 1",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("saved trace lacks %q:\n%s", want, data)
		}
	}
	if !strings.Contains(path, "logic-analyze-trace-") || !strings.HasSuffix(path, "-test-board.txt") {
		t.Fatalf("unexpected path %q", path)
	}
}
