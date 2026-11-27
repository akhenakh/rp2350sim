package devices

import (
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func TestModulePullUpSurvivesReset(t *testing.T) {
	c := rp2350.New()
	NewWire(c, oneWirePin).EnablePullUp()
	if !c.Pins.Level(oneWirePin) {
		t.Fatal("module pull-up did not raise the line")
	}
	c.Reset()
	if !c.Pins.Level(oneWirePin) {
		t.Fatal("module pull-up was lost on reset")
	}
}
