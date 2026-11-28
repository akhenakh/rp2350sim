package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akhenakh/rp2350sim/internal/gdb"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func TestMain(m *testing.M) {
	if os.Getenv("RP2350SIM_GDB_TEST_HELPER") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestGDBTargetBasics(t *testing.T) {
	c := rp2350.New()
	var mu sync.Mutex
	tgt := &gdbTarget{chip: c, mu: &mu}

	if err := tgt.WriteMemory(0x20000000, []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	got, err := tgt.ReadMemory(0x20000000, 4)
	if err != nil || string(got) != "\x01\x02\x03\x04" {
		t.Fatalf("memory = % x, %v", got, err)
	}
	regs, err := tgt.ReadRegisters(0)
	if err != nil || len(regs) != gdb.RegisterCount {
		t.Fatalf("registers = %d, %v", len(regs), err)
	}
	if err := tgt.WriteRegister(0, 15, 0x20000001); err != nil {
		t.Fatal(err)
	}
	if c.CPU0.R[15] != 0x20000000 {
		t.Fatalf("PC = %#x", c.CPU0.R[15])
	}

	if err := tgt.AddBreakpoint(0x20000010); err != nil {
		t.Fatal(err)
	}
	if err := tgt.Halt(0); err != nil {
		t.Fatal(err)
	}
	core, _, reason, _, ok := tgt.Stopped()
	if !ok || core != 0 || reason != "interrupt" {
		t.Fatalf("stopped = %v core=%d reason=%q", ok, core, reason)
	}

	// Step one NOP from SRAM.
	c.Write16(0x20000000, 0xBF00)
	c.CPU0.R[15] = 0x20000000
	if err := tgt.Step(0); err != nil {
		t.Fatal(err)
	}
	if c.CPU0.R[15] != 0x20000002 {
		t.Fatalf("step PC = %#x", c.CPU0.R[15])
	}
	if _, _, _, _, ok := tgt.Stopped(); !ok {
		t.Fatal("step did not leave the target stopped")
	}
	tgt.Continue()
	if _, _, _, _, ok := tgt.Stopped(); ok {
		t.Fatal("continue left the target stopped")
	}

	// MMIO reads needed for watchpoint expressions fall back to the bus.
	if _, err := tgt.ReadMemory(0x400B0000, 4); err != nil {
		t.Fatalf("MMIO read: %v", err)
	}
}

func TestCLIGDBInputAndFinalizationRace(t *testing.T) {
	fwPath := filepath.Join(t.TempDir(), "loop.bin")
	fw := make([]byte, 512)
	binary.LittleEndian.PutUint32(fw[0:4], rp2350.SRAMBase+0x1000)
	binary.LittleEndian.PutUint32(fw[4:8], rp2350.FlashBase+0x101)
	binary.LittleEndian.PutUint16(fw[0x100:0x102], 0xE7FE) // b .
	if err := os.WriteFile(fwPath, fw, 0o644); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	summaryPath := filepath.Join(t.TempDir(), "summary.json")
	flashPath := filepath.Join(t.TempDir(), "flash.bin")
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0],
		"-fw", fwPath,
		"-gdb", addr,
		"-steps", "20000000",
		"-input-at", "1s",
		"-input", "hello",
		"-peek", "20000000",
		"-summary-file", summaryPath,
		"-flash-out", flashPath,
	)
	cmd.Env = append(os.Environ(), "RP2350SIM_GDB_TEST_HELPER=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	var conn net.Conn
	for conn == nil {
		var dialErr error
		conn, dialErr = net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if dialErr == nil {
			break
		}
		select {
		case <-ctx.Done():
			_ = cmd.Wait()
			t.Fatalf("gdb listener did not start: %v\n%s", dialErr, stderr.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	if err := writeRSPPacket(conn, r, "c"); err != nil {
		t.Fatal(err)
	}
	replies := 0
	for i := range 64 {
		packet := "g"
		if i%2 != 0 {
			packet = "m20000000,4"
		}
		if err := writeRSPPacket(conn, r, packet); err != nil {
			if replies >= 8 || errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		if _, err := readRSPReply(r); err != nil {
			if replies >= 8 || errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		replies++
	}
	if replies < 8 {
		t.Fatalf("only %d GDB replies before finalization", replies)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("runner failed: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "DATA RACE") {
		t.Fatalf("race detected:\n%s", stderr.String())
	}
	summary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(summary, []byte(`"exit_code": 0`)) {
		t.Fatalf("unexpected summary: %s", summary)
	}
	if info, err := os.Stat(flashPath); err != nil || info.Size() != rp2350.FlashSize {
		t.Fatalf("flash output = %v, %v", info, err)
	}
}

func writeRSPPacket(conn net.Conn, r *bufio.Reader, packet string) error {
	var sum byte
	for i := range len(packet) {
		sum += packet[i]
	}
	if _, err := fmt.Fprintf(conn, "$%s#%02x", packet, sum); err != nil {
		return err
	}
	ack, err := r.ReadByte()
	if err != nil {
		return err
	}
	if ack != '+' {
		return fmt.Errorf("rsp ack = %#x", ack)
	}
	return nil
}

func readRSPReply(r *bufio.Reader) (string, error) {
	for {
		b, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		if b != '$' {
			continue
		}
		var payload strings.Builder
		for {
			c, err := r.ReadByte()
			if err != nil {
				return "", err
			}
			if c == '#' {
				break
			}
			payload.WriteByte(c)
		}
		if _, err := r.Discard(2); err != nil {
			return "", err
		}
		return payload.String(), nil
	}
}
