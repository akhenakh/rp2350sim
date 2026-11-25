package main

import (
	"debug/elf"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func TestLoadFirmwarePreservesFlashBacking(t *testing.T) {
	const markerOffset = 0x10000
	marker := []byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0}
	backing := filepath.Join(t.TempDir(), "flash.bin")
	data := make([]byte, markerOffset+len(marker))
	for i := range data {
		data[i] = 0xff
	}
	copy(data[markerOffset:], marker)
	if err := os.WriteFile(backing, data, 0o644); err != nil {
		t.Fatal(err)
	}

	fwPath := writeFlashELF(t)
	e := newTestEmu()
	e.SetFlashPath(backing)
	if err := e.Load(fwPath); err != nil {
		t.Fatal(err)
	}
	if got := e.chip.Flash[markerOffset : markerOffset+len(marker)]; string(got) != string(marker) {
		t.Fatalf("marker after ELF load = % x, want % x", got, marker)
	}
	if got := binary.LittleEndian.Uint32(e.chip.Flash[4:8]); got != rp2350.FlashBase+0x101 {
		t.Fatalf("reset vector after ELF load = %#x", got)
	}

	e.SaveFlash()
	saved, err := os.ReadFile(backing)
	if err != nil {
		t.Fatal(err)
	}
	if got := saved[markerOffset : markerOffset+len(marker)]; string(got) != string(marker) {
		t.Fatalf("marker after save = % x, want % x", got, marker)
	}
	if got := binary.LittleEndian.Uint32(saved[4:8]); got != rp2350.FlashBase+0x101 {
		t.Fatalf("saved reset vector = %#x", got)
	}
}

func TestLoadFirmwareMissingFlashBackingStartsErased(t *testing.T) {
	backing := filepath.Join(t.TempDir(), "missing.bin")
	e := newTestEmu()
	e.SetFlashPath(backing)
	if err := e.Load(writeFlashELF(t)); err != nil {
		t.Fatal(err)
	}
	if got := e.chip.Flash[0x10000]; got != 0xff {
		t.Fatalf("unused flash byte = %#x, want 0xff", got)
	}
	e.SaveFlash()
	info, err := os.Stat(backing)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != rp2350.FlashSize {
		t.Fatalf("saved flash size = %d, want %d", info.Size(), rp2350.FlashSize)
	}
}

func TestEmuResetClosesEthernetListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	addr := "127.0.0.1:" + strconv.Itoa(port)

	e := newTestEmu()
	e.SetBoard(&BoardDef{ID: "eth", Ethernet: &EthDevice{Kind: "w5500", SPI: 0, CS: 17}})
	old := e.w5500
	if old == nil {
		t.Fatal("W5500 was not attached")
	}
	c := e.chip
	c.Pins.SetExternal(17, false)
	write := func(off byte, data ...byte) {
		frame := append([]byte{0, off, 0x0c}, data...) // socket 0 registers, write
		for _, b := range frame {
			old.Transfer(b)
		}
		c.Pins.SetExternal(17, true)
		old.Tick()
		c.Pins.SetExternal(17, false)
	}
	write(0x00, 1) // TCP
	write(0x04, byte(port>>8), byte(port))
	write(0x01, 2) // LISTEN
	deadline := time.Now().Add(2 * time.Second)
	for {
		sockets := old.NetworkInfo().Sockets
		if len(sockets) == 1 && sockets[0].HostAddr != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("W5500 listener did not start")
		}
		time.Sleep(time.Millisecond)
	}

	e.Reset()
	if e.w5500 == old || e.w5500 == nil {
		t.Fatal("reset did not replace the W5500")
	}
	ln, err = net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("reset left the W5500 port bound: %v", err)
	}
	_ = ln.Close()
}

func newTestEmu() *Emu {
	e := &Emu{debugger: rp2350.NewDebugger()}
	e.mu.Lock()
	e.resetChip()
	e.mu.Unlock()
	return e
}

func writeFlashELF(t *testing.T) string {
	t.Helper()
	const headerSize = 52
	const programHeaderSize = 32
	segment := make([]byte, 256)
	binary.LittleEndian.PutUint32(segment[0:4], rp2350.SRAMBase+0x1000)
	binary.LittleEndian.PutUint32(segment[4:8], rp2350.FlashBase+0x101)

	image := make([]byte, headerSize+programHeaderSize+len(segment))
	copy(image[0:16], "\x7fELF")
	image[4] = byte(elf.ELFCLASS32)
	image[5] = byte(elf.ELFDATA2LSB)
	image[6] = byte(elf.EV_CURRENT)
	binary.LittleEndian.PutUint16(image[16:18], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(image[18:20], uint16(elf.EM_ARM))
	binary.LittleEndian.PutUint32(image[20:24], uint32(elf.EV_CURRENT))
	binary.LittleEndian.PutUint32(image[24:28], rp2350.FlashBase+0x101)
	binary.LittleEndian.PutUint32(image[28:32], headerSize)
	binary.LittleEndian.PutUint16(image[40:42], headerSize)
	binary.LittleEndian.PutUint16(image[42:44], programHeaderSize)
	binary.LittleEndian.PutUint16(image[44:46], 1)

	ph := image[headerSize:]
	binary.LittleEndian.PutUint32(ph[0:4], uint32(elf.PT_LOAD))
	binary.LittleEndian.PutUint32(ph[4:8], headerSize+programHeaderSize)
	binary.LittleEndian.PutUint32(ph[8:12], rp2350.FlashBase)
	binary.LittleEndian.PutUint32(ph[12:16], rp2350.FlashBase)
	binary.LittleEndian.PutUint32(ph[16:20], uint32(len(segment)))
	binary.LittleEndian.PutUint32(ph[20:24], uint32(len(segment)))
	binary.LittleEndian.PutUint32(ph[24:28], uint32(elf.PF_R|elf.PF_X))
	binary.LittleEndian.PutUint32(ph[28:32], 4)
	copy(image[headerSize+programHeaderSize:], segment)

	path := filepath.Join(t.TempDir(), "firmware.elf")
	if err := os.WriteFile(path, image, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCycleDuration(t *testing.T) {
	if got := cycleDuration(150_000); got != time.Millisecond {
		t.Fatalf("cycleDuration(150000) = %v, want 1ms", got)
	}
	if got := cycleDuration(rp2350.SysClkHz); got != time.Second {
		t.Fatalf("cycleDuration(1s of cycles) = %v, want 1s", got)
	}
}

func TestPaceSleep(t *testing.T) {
	if got := paceSleep(10*time.Millisecond, 4*time.Millisecond); got != 6*time.Millisecond {
		t.Fatalf("ahead by 6ms = %v, want 6ms", got)
	}
	if got := paceSleep(4*time.Millisecond, 10*time.Millisecond); got != 0 {
		t.Fatalf("behind by 6ms = %v, want no sleep", got)
	}
	if got := paceSleep(time.Second, 0); got != 20*time.Millisecond {
		t.Fatalf("a long lead = %v, want the 20ms cap", got)
	}
}

// Pacing keeps virtual time level with wall time; turbo removes the wait. The
// indicator must agree with the measured ratio.
func TestRealtimePacingAndTurbo(t *testing.T) {
	fw := filepath.Join("..", "..", "build", "blinky.elf")
	if _, err := os.Stat(fw); err != nil {
		t.Skip("build/blinky.elf is not built")
	}
	defs, err := LoadBoards()
	if err != nil {
		t.Fatal(err)
	}
	var board *BoardDef
	for i := range defs {
		if defs[i].ID == "pico2" {
			board = &defs[i]
		}
	}
	if board == nil {
		t.Fatal("pico2 board is missing")
	}

	e := NewEmu()
	defer e.SetRunning(false)
	e.SetBoard(board)
	if err := e.Load(fw); err != nil {
		t.Fatal(err)
	}

	time.Sleep(1200 * time.Millisecond)
	paced := e.Snapshot()
	if !paced.Running || !paced.Pacing {
		t.Fatalf("running %v pacing %v, want both true", paced.Running, paced.Pacing)
	}
	// Under -race the interpreter can be far slower than the host clock, so
	// only the upper bound is guaranteed: pacing must not let it race ahead.
	if paced.Speed <= 0 || paced.Speed > 1.5 {
		t.Fatalf("paced speed %.2f, want at most 1.5", paced.Speed)
	}
	if paced.Slow && paced.Speed > 0.97 {
		t.Fatalf("slow %v with speed %.2f, want cleared above 0.97", paced.Slow, paced.Speed)
	}
	if !paced.Slow && paced.Speed < 0.90 {
		t.Fatalf("not slow with speed %.2f, want set below 0.90", paced.Speed)
	}

	e.SetTurbo(true)
	time.Sleep(1200 * time.Millisecond)
	turbo := e.Snapshot()
	if turbo.Pacing {
		t.Fatal("turbo mode still paces")
	}
	if turbo.Slow {
		t.Fatal("turbo mode cannot be reported as slow")
	}
	// The windows are independent; allow the measurement noise of a loaded
	// or race-enabled runner.
	if turbo.Speed+0.05 < paced.Speed {
		t.Fatalf("turbo speed %.2f < paced %.2f", turbo.Speed, paced.Speed)
	}
}
