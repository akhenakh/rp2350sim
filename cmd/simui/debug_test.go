package main

import (
	"os"
	"testing"
)

func TestDebugPanelSnapshot(t *testing.T) {
	const path = "../../firmware/c/build/debug.elf"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s is not built", path)
	}
	e := NewEmu()
	e.SetBoard(&BoardDef{ID: "pico2", Name: "Pico 2"})
	if err := e.Load(path); err != nil {
		t.Fatal(err)
	}
	e.SetRunning(false)
	if _, err := e.AddBreakpoint("debug_marker"); err != nil {
		t.Fatal(err)
	}
	e.RunFor(105_000_000) // 0.7 s virtual: startup plus the 100 ms sleep
	s := e.DebugSnapshot()
	if !s.Stopped || s.Function != "debug_marker" {
		t.Fatalf("stopped=%v reason=%q function=%q pc=%#x", s.Stopped, s.StopReason, s.Function, s.PC)
	}
	found := false
	for _, v := range s.Globals {
		if v.Name == "g_magic" {
			found = true
			if v.Value != "0x12345678" {
				t.Fatalf("g_magic = %q", v.Value)
			}
		}
	}
	if !found {
		t.Fatal("g_magic missing from DebugSnapshot globals")
	}
	for range 6 {
		e.StepDebug(0)
	}
	s = e.DebugSnapshot()
	for _, v := range s.Locals {
		if v.Name == "local" {
			if v.Value != "0xaaaaaaaa" {
				t.Fatalf("local = %q", v.Value)
			}
			return
		}
	}
	t.Fatalf("local missing after stepping; locals=%+v", s.Locals)
}
