package main

import (
	"encoding/binary"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func uf2Block(addr uint32, payload []byte, family uint32) []byte {
	blk := make([]byte, 512)
	binary.LittleEndian.PutUint32(blk[0:], 0x0A324655)
	binary.LittleEndian.PutUint32(blk[4:], 0x9E5D5157)
	binary.LittleEndian.PutUint32(blk[8:], 0x2000) // family present
	binary.LittleEndian.PutUint32(blk[12:], addr)
	binary.LittleEndian.PutUint32(blk[16:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(blk[20:], 0)
	binary.LittleEndian.PutUint32(blk[24:], 0)
	binary.LittleEndian.PutUint32(blk[28:], family)
	copy(blk[32:], payload)
	binary.LittleEndian.PutUint32(blk[508:], 0x0AB16F30)
	return blk
}

func TestFlashUF2BootselAcceptsAnyFamily(t *testing.T) {
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = 0xff
	}
	binary.LittleEndian.PutUint32(payload[0:], rp2350.SRAMBase+0x1000)
	binary.LittleEndian.PutUint32(payload[4:], rp2350.FlashBase+8+1)
	binary.LittleEndian.PutUint16(payload[8:], 0xe7fe) // b .
	uf2 := uf2Block(rp2350.FlashBase, payload, 0x12345678)

	e := newTestEmu()
	if err := e.FlashUF2("test.uf2", uf2); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(e.chip.Flash[4:]); got != rp2350.FlashBase+8+1 {
		t.Fatalf("reset vector = %#x", got)
	}
	if e.chip.BootCount() != 1 {
		t.Fatalf("boot count = %d", e.chip.BootCount())
	}
	if !e.running {
		t.Fatal("flashed image was not started")
	}
}

func TestFlashUF2RejectsNonFlash(t *testing.T) {
	payload := make([]byte, 256)
	uf2 := uf2Block(rp2350.SRAMBase, payload, 0xe48bff59)
	if err := newTestEmu().FlashUF2("bad.uf2", uf2); err == nil {
		t.Fatal("SRAM-target UF2 was accepted")
	}
}
