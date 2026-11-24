package main

import (
	"log/slog"
	"reflect"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func TestHeadlessPinCallbacksCompose(t *testing.T) {
	c := rp2350.New()
	var events []string
	c.Pins.OnChange = func(pin int, level bool) {
		if pin == 17 {
			if level {
				events = append(events, "previous-high")
			} else {
				events = append(events, "previous-low")
			}
		}
	}
	specs, err := parseBusPresetSpecs("spi", []string{"0:w25q16@17"})
	if err != nil {
		t.Fatal(err)
	}
	var transfer func(byte) byte
	if err := attachBusPresets(c, slog.New(slog.DiscardHandler), specs, func(_, _ int, fn func(byte) byte) {
		transfer = fn
	}); err != nil {
		t.Fatal(err)
	}
	c.Pins.OnChange = chainPinCallbacks(c.Pins.OnChange, func(pin int, level bool) {
		if pin == 17 {
			if level {
				events = append(events, "headless-high")
			} else {
				events = append(events, "headless-low")
			}
		}
	})

	c.Pins.SetExternal(17, true)
	c.Pins.SetExternal(17, false)
	want := []string{"previous-high", "headless-high", "previous-low", "headless-low"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("pin callbacks = %v, want %v", events, want)
	}
	if transfer == nil || transfer(0x9f) != 0xef {
		t.Fatal("preset did not observe the composed CS-low edge")
	}
}

func TestChainPinCallbacksNil(t *testing.T) {
	called := false
	next := func(int, bool) { called = true }
	if got := chainPinCallbacks(nil, next); got == nil {
		t.Fatal("nil previous callback discarded the next callback")
	} else {
		got(1, true)
	}
	if !called {
		t.Fatal("next callback was not called")
	}
	if got := chainPinCallbacks(nil, nil); got != nil {
		t.Fatal("two nil callbacks produced a non-nil callback")
	}
}
