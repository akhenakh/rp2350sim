package ch9120

import (
	"bytes"
	"io"
	"testing"
)

type fakeUART struct {
	out []byte
	in  []byte
}

func (f *fakeUART) Read(p []byte) (int, error) {
	if len(f.in) == 0 {
		return 0, nil
	}
	n := copy(p, f.in)
	f.in = f.in[n:]
	return n, nil
}

func (f *fakeUART) ReadByte() (byte, error) {
	if len(f.in) == 0 {
		return 0, io.EOF
	}
	b := f.in[0]
	f.in = f.in[1:]
	return b, nil
}

func (f *fakeUART) Write(p []byte) (int, error) {
	f.out = append(f.out, p...)
	f.in = append(f.in, 0xaa)
	return len(p), nil
}

type fakePin struct{ high bool }

func (p *fakePin) Set(high bool) { p.high = high }
func (p *fakePin) Get() bool     { return p.high }

func TestConfigureSequence(t *testing.T) {
	u := &fakeUART{}
	cfg0, rst, tcps := &fakePin{}, &fakePin{}, &fakePin{high: true}
	var bauds []uint32
	d := New(u, cfg0, rst, tcps)
	err := d.Configure(Config{
		Mode:       TCPClient,
		LocalIP:    [4]byte{192, 168, 1, 200},
		SubnetMask: [4]byte{255, 255, 255, 0},
		Gateway:    [4]byte{192, 168, 1, 1},
		TargetIP:   [4]byte{127, 0, 0, 1},
		LocalPort:  2000,
		TargetPort: 9000,
		Baud:       115200,
		Reconfigure: func(b uint32) {
			bauds = append(bauds, b)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bauds) < 2 || bauds[0] != 9600 || bauds[len(bauds)-1] != 115200 {
		t.Fatalf("baud sequence = %v", bauds)
	}
	for _, frame := range [][]byte{
		{0x57, 0xab, 0x10, 0x01},
		{0x57, 0xab, 0x11, 192, 168, 1, 200},
		{0x57, 0xab, 0x14, 0x07, 0xd0},
		{0x57, 0xab, 0x16, 0x23, 0x28},
		{0x57, 0xab, 0x0e},
	} {
		if !bytes.Contains(u.out, frame) {
			t.Fatalf("missing frame % x in % x", frame, u.out)
		}
	}
	if !cfg0.high {
		t.Fatal("CFG0 not released to data mode")
	}
	if d.Connected() {
		t.Fatal("TCPCS low before the module connects")
	}
	tcps.high = false
	if !d.Connected() {
		t.Fatal("TCPCS low should mean connected")
	}
}
