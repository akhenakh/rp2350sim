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
	c.SendUSBCDC([]byte("hi"))

	out := c.Read32(0x50100000 + usbBufCtrlOffOut(2))
	if got := out & 0x3FF; got != 2 {
		t.Fatalf("EP2 OUT length = %d, want 2", got)
	}
	if out&usbBufFull == 0 {
		t.Fatal("EP2 OUT buffer not marked full")
	}
	if in := c.Read32(0x50100000 + usbBufCtrlOff(2)); in&usbBufFull != 0 {
		t.Fatalf("EP2 IN control armed by SendCDC: %#x", in)
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
	c.usb.setWord(usbBufCtrlOff(3), 4|usbBufFull)
	c.usb.pollEndpoints(c)
	if string(got) != string([]byte{0x08, 0x80, 60, 0}) {
		t.Fatalf("MIDI IN callback = % x", got)
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
	u.setWord(usbBufCtrlOff(0), 4|usbBufFull)
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
	if u.step != 5 {
		t.Fatalf("step = %d, want the transfer to finish at step 5", u.step)
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
