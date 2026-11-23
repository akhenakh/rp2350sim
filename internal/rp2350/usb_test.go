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

// TestUSBCdcOutControl checks that host-to-device data lands in the OUT buffer
// control: the guest reads the transfer length from there, and the IN control
// must stay unarmed or the host drains a phantom IN packet.
func TestUSBCdcOutControl(t *testing.T) {
	c := New()
	// The device arms the OUT buffer before the host may fill it.
	c.usb.setWord(usbBufCtrlOffOut(2), usbBufAvail)
	c.SendUSBCDC([]byte("hi"))

	out := c.Read32(0x50100000 + usbBufCtrlOffOut(2))
	if got := out & 0x3FF; got != 2 {
		t.Fatalf("EP2 OUT length = %d, want 2", got)
	}
	if out&usbBufFull == 0 {
		t.Fatal("EP2 OUT buffer not marked full")
	}
	if out&usbBufData1 != 0 {
		t.Fatal("first bulk OUT packet should be DATA0")
	}
	if in := c.Read32(0x50100000 + usbBufCtrlOff(2)); in&usbBufFull != 0 {
		t.Fatalf("EP2 IN control armed by SendCDC: %#x", in)
	}

	// The guest consumes the packet and re-arms; the next one is DATA1.
	c.usb.setWord(usbBufCtrlOffOut(2), usbBufAvail)
	c.SendUSBCDC([]byte("yo"))
	out = c.Read32(0x50100000 + usbBufCtrlOffOut(2))
	if out&usbBufData1 == 0 {
		t.Fatal("second bulk OUT packet should be DATA1")
	}
}

// TestUSBMIDIEndpoints checks the EP3 buffers and the IN routing to the MIDI
// callback rather than the CDC console.
func TestUSBMIDIEndpoints(t *testing.T) {
	c := New()
	c.EnableUSBMIDI()
	if c.SendUSBMIDI([]byte{0x09, 0x90, 60, 100}) {
		t.Fatal("MIDI packet accepted before enumeration finished")
	}
	c.usb.state = usbHostRunning
	pkt := []byte{0x09, 0x90, 60, 100}
	c.usb.setWord(usbBufCtrlOffOut(3), usbBufAvail)
	if !c.SendUSBMIDI(pkt) {
		t.Fatal("SendUSBMIDI refused an armed endpoint")
	}
	out := c.usb.word(usbBufCtrlOffOut(3))
	if out&usbBufFull == 0 || out&0x3FF != 4 {
		t.Fatalf("EP3 OUT control = %#x, want full/4", out)
	}
	if in := c.usb.word(usbBufCtrlOff(3)); in&usbBufFull != 0 {
		t.Fatalf("EP3 IN control armed by SendUSBMIDI: %#x", in)
	}
	if got := c.usb.readBytes(usbBufDataOff(3, false), 4); string(got) != string(pkt) {
		t.Fatalf("EP3 OUT data = % x", got)
	}

	var got []byte
	c.SetUSBMIDI(func(b []byte) { got = append([]byte(nil), b...) })
	c.usb.writeBytes(usbBufDataOff(3, true), []byte{0x08, 0x80, 60, 0})
	c.usb.setWord(usbBufCtrlOff(3), 4|usbBufFull|usbBufAvail)
	c.usb.pollEndpoints(c)
	if string(got) != string([]byte{0x08, 0x80, 60, 0}) {
		t.Fatalf("MIDI IN callback = % x", got)
	}
	// The completion keeps the length but clears FULL/AVAIL, which TinyUSB's
	// transfer accounting reads back.
	if ctrl := c.usb.word(usbBufCtrlOff(3)); ctrl != 4 {
		t.Fatalf("EP3 IN completion control = %#x, want length 4 only", ctrl)
	}
}

// TestUSBControlWaitsForCompletionAck guards the multi-packet control path:
// TinyGo sends the next EP0 chunk from the BUFF_STATUS ISR, so the host must
// not complete a new packet before the firmware clears the previous bit. The
// composite CDC+MIDI descriptor (175 bytes) is the first transfer long enough
// to hit this.
func TestUSBControlWaitsForCompletionAck(t *testing.T) {
	c := New()
	u := c.usb
	u.state = usbHostControl
	u.step = 4 // GET_DESCRIPTOR configuration, 255 bytes
	u.buffStatus = 1
	u.setWord(usbBufCtrlOff(0), 4|usbBufFull|usbBufAvail)
	u.writeBytes(usbBufDataOff(0, true), []byte{1, 2, 3, 4})

	u.pollControl()
	if got := u.word(usbBufCtrlOff(0)); got&usbBufFull == 0 {
		t.Fatal("host completed an EP0 packet before the firmware acked BUFF_STATUS")
	}
	u.write(c, usbBUFFSTATUS, 1)
	u.pollControl()
	if got := u.word(usbBufCtrlOff(0)); got&usbBufFull != 0 {
		t.Fatalf("host did not complete the EP0 packet after the ack: %#x", got)
	}
	if !u.needStatus {
		t.Fatal("control-IN data stage did not request an OUT status packet")
	}
	if u.step != 4 {
		t.Fatalf("step = %d, want to wait in the data stage at step 4", u.step)
	}

	// Arm EP0 OUT: the host sends the zero-length status packet, always as
	// DATA1 with AVAIL cleared.
	u.buffStatus = 0 // the firmware acknowledged the IN completion
	u.setWord(usbBufCtrlOffOut(0), usbBufAvail)
	u.pollControl()
	out := u.word(usbBufCtrlOffOut(0))
	if out&usbBufFull == 0 || out&usbBufData1 == 0 || out&usbBufAvail != 0 {
		t.Fatalf("EP0 status = %#x, want FULL|DATA1 without AVAIL", out)
	}
	// The firmware handles the OUT completion (BUFF_STATUS bit 1); the host
	// can then finish the request.
	u.buffStatus = 0
	u.pollControl()
	if u.step != 5 {
		t.Fatalf("step = %d, want the transfer to finish at step 5", u.step)
	}
}

// TestUSBParseConfig checks that the host follows the configuration descriptor
// instead of assuming TinyGo's EP2/EP3 numbering (pico-sdk TinyUSB may use any
// free endpoint numbers).
func TestUSBParseConfig(t *testing.T) {
	c := New()
	u := c.usb
	desc := []byte{
		9, 2, 0, 0, 3, 1, 0, 0x80, 50, // configuration, 3 interfaces
		9, 4, 0, 0, 1, 0x02, 0x02, 0x00, 0, // CDC control
		7, 5, 0x81, 0x03, 8, 0, 0xFF, // notification EP1 IN interrupt
		9, 4, 1, 0, 2, 0x0A, 0x00, 0x00, 0, // CDC data
		7, 5, 0x04, 0x02, 64, 0, 0, // EP4 OUT bulk
		7, 5, 0x85, 0x02, 64, 0, 0, // EP5 IN bulk
		9, 4, 2, 0, 2, 0x01, 0x03, 0x00, 0, // MIDI streaming
		7, 5, 0x06, 0x02, 64, 0, 0, // EP6 OUT bulk
		7, 5, 0x87, 0x02, 64, 0, 0, // EP7 IN bulk
	}
	desc[2] = byte(len(desc))
	u.parseConfig(desc)
	if u.cdcNotify != 1 || u.cdcOut != 4 || u.cdcIn != 5 || u.midiOut != 6 || u.midiIn != 7 {
		t.Fatalf("endpoints = notify %d, cdc %d/%d, midi %d/%d",
			u.cdcNotify, u.cdcOut, u.cdcIn, u.midiOut, u.midiIn)
	}
}

// TestUSBEndpointControlAddress checks that buffers are read from the address
// the firmware programmed in EP_CONTROL, with the TinyGo layout as fallback.
func TestUSBEndpointControlAddress(t *testing.T) {
	c := New()
	u := c.usb
	if got := u.epDataAddr(2, true); got != 0x240 {
		t.Fatalf("fallback IN address = %#x, want 0x240", got)
	}
	// EP2 IN control lives at 0x008 + (2-1)*8; OUT at +4.
	u.setWord(0x008+8, usbEpCtrlEnable|0x700)
	u.setWord(0x00C+8, usbEpCtrlEnable|0x680)
	if got := u.epDataAddr(2, true); got != 0x700 {
		t.Fatalf("EP_CONTROL IN address = %#x, want 0x700", got)
	}
	if got := u.epDataAddr(2, false); got != 0x680 {
		t.Fatalf("EP_CONTROL OUT address = %#x, want 0x680", got)
	}
	// An out-of-range address falls back to the TinyGo layout.
	u.setWord(0x008+8, usbEpCtrlEnable|0xFF0)
	if got := u.epDataAddr(2, true); got != 0x240 {
		t.Fatalf("out-of-range address = %#x, want fallback 0x240", got)
	}
	// The top 64 bytes fit a single-buffered endpoint.
	u.setWord(0x008+8, usbEpCtrlEnable|0xFC0)
	if got := u.epDataAddr(2, true); got != 0xFC0 {
		t.Fatalf("top-of-DPRAM address = %#x, want 0xFC0", got)
	}
	// A double-buffered endpoint needs 128 bytes and must fall back.
	u.setWord(0x008+8, usbEpCtrlEnable|usbEpCtrlDouble|0xFC0)
	if got := u.epDataAddr(2, true); got != 0x240 {
		t.Fatalf("double-buffered top address = %#x, want fallback 0x240", got)
	}
}

// TestUSBBufferBounds checks that an oversized length near the end of DPRAM
// is clamped instead of indexing past the register file.
func TestUSBBufferBounds(t *testing.T) {
	c := New()
	u := c.usb
	u.writeBytes(0xFFE, []byte{1, 2, 3, 4, 5})
	if got := u.readBytes(0xFFE, 8); len(got) != 8 || got[0] != 1 || got[1] != 2 || got[2] != 0 {
		t.Fatalf("clamped read = % x", got)
	}
}

// TestUSBTakeINDoubleBuffer checks that two TinyUSB bulk-IN packets queued in
// the double-buffer halves are drained together, preserving both lengths.
func TestUSBTakeINDoubleBuffer(t *testing.T) {
	c := New()
	u := c.usb
	u.setWord(0x008+16, usbEpCtrlEnable|usbEpCtrlDouble|0x700) // EP3 IN
	u.writeBytes(0x700, []byte{1, 2, 3, 4})
	u.writeBytes(0x740, []byte{5, 6, 7, 8})
	u.setWord(usbBufCtrlOff(3), 4|usbBufFull|((4|usbBufFull)<<usbBuf1Shift))
	got := u.takeIN(3)
	if string(got) != string([]byte{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("double-buffered payload = % x", got)
	}
	want := uint32(4) | uint32(4)<<usbBuf1Shift
	if ctrl := u.word(usbBufCtrlOff(3)); ctrl != want {
		t.Fatalf("completion control = %#x, want %#x", ctrl, want)
	}
	if u.buffStatus&(1<<6) == 0 {
		t.Fatal("EP3 IN completion did not raise BUFF_STATUS")
	}
}

// TestUSBInterruptLatches checks the write-1-to-clear interrupt path used by the
// firmware's USB handler.
func TestUSBInterruptLatches(t *testing.T) {
	c := New()
	u := c.usb
	u.regs.regs[usbINTE/4] = usbINTSetupReq | usbINTBuffStatus
	u.intr = usbINTSetupReq
	u.buffStatus = 1 << 1
	u.intr |= usbINTBuffStatus

	if got, _ := u.read(c, usbINTS); got != usbINTSetupReq|usbINTBuffStatus {
		t.Fatalf("INTS = %#x", got)
	}
	// Clearing BUFF_STATUS clears the buffer interrupt.
	u.write(c, usbBUFFSTATUS, 1<<1)
	if u.intr&usbINTBuffStatus != 0 {
		t.Fatal("BUFF_STATUS interrupt did not clear")
	}
	// Clearing SIE_STATUS.SETUP_REC clears SETUP_REQ.
	u.setSIEStatus(usbSIESetupRec)
	u.write(c, usbSIESTATUS, usbSIESetupRec)
	if u.intr&usbINTSetupReq != 0 {
		t.Fatal("SETUP_REQ interrupt did not clear")
	}
}

// TestUSBAttachViaSetAlias checks that TinyUSB's use of the +0x2000 SET alias
// for SIE_CTRL.PULLUP_EN reaches the controller.
func TestUSBAttachViaSetAlias(t *testing.T) {
	c := New()
	c.Write32(usbRegsBase+usbMAINCTRL, usbMainEnable)
	c.Write32(usbRegsBase+0x2000+usbSIECTRL, usbSIEPullupEn)
	if !c.usb.attached {
		t.Fatal("USB did not attach through the SET alias")
	}
	if !c.usb.enabled() {
		t.Fatal("USB controller not enabled")
	}
}
