package rp2350

import "testing"

// TestUSBDPRAMAccess checks that the DPRAM window behaves like byte-addressed
// memory through the bus (the firmware writes endpoint buffers with copy()).
func TestUSBDPRAMAccess(t *testing.T) {
	c := New()
	want := []byte{0x11, 0x22, 0x33, 0x44, 0x55}
	c.usb.writeBytes(usbBufDataOff(2, false), want)

	for i, b := range want {
		if got := c.Read8(0x50100000 + usbBufDataOff(2, false) + uint32(i)); got != b {
			t.Fatalf("byte %d = %#02x, want %#02x", i, got, b)
		}
	}
	c.Write8(0x50100000+usbBufDataOff(2, false)+1, 0xA5)
	if got := c.Read8(0x50100000 + usbBufDataOff(2, false) + 1); got != 0xA5 {
		t.Fatalf("byte write through bus = %#02x", got)
	}
}

// TestUSBInterruptLatches checks the write-1-to-clear interrupt path used by the
// firmware's USB handler.
func TestUSBInterruptLatches(t *testing.T) {
	c := New()
	u := c.usb
	u.block.regs[usbReg(usbINTE)/4] = usbINTSetupReq | usbINTBuffStatus
	u.intr = usbINTSetupReq
	u.buffStatus = 1 << 1
	u.intr |= usbINTBuffStatus

	if got, _ := u.read(c, usbReg(usbINTS)); got != usbINTSetupReq|usbINTBuffStatus {
		t.Fatalf("INTS = %#x", got)
	}
	// Clearing BUFF_STATUS clears the buffer interrupt.
	u.write(c, usbReg(usbBUFFSTATUS), 1<<1)
	if u.intr&usbINTBuffStatus != 0 {
		t.Fatal("BUFF_STATUS interrupt did not clear")
	}
	// Clearing SIE_STATUS.SETUP_REC clears SETUP_REQ.
	u.setSIEStatus(usbSIESetupRec)
	u.write(c, usbReg(usbSIESTATUS), usbSIESetupRec)
	if u.intr&usbINTSetupReq != 0 {
		t.Fatal("SETUP_REQ interrupt did not clear")
	}
}
