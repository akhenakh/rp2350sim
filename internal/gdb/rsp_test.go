package gdb

import (
	"bufio"
	"errors"
	"net"
	"strings"
	"testing"
)

type fakeTarget struct {
	mem     []byte
	regs    [2][]uint32
	bp      map[uint32]bool
	watch   map[uint32]int
	stopped bool
	reason  string
	pc      uint32
	core    int
	steps   int
}

func newFakeTarget() *fakeTarget {
	f := &fakeTarget{bp: map[uint32]bool{}, watch: map[uint32]int{}, stopped: true, reason: "breakpoint", pc: 0x1000, mem: make([]byte, 64)}
	for c := range 2 {
		f.regs[c] = make([]uint32, RegisterCount)
		f.regs[c][0] = uint32(0x100 + c)
		f.regs[c][16] = 0x01000000
	}
	return f
}

func (f *fakeTarget) ReadMemory(addr uint32, size int) ([]byte, error) {
	if addr+uint32(size) > uint32(len(f.mem)) {
		return nil, errors.New("unmapped")
	}
	out := make([]byte, size)
	copy(out, f.mem[addr:])
	return out, nil
}

func (f *fakeTarget) WriteMemory(addr uint32, data []byte) error {
	if addr+uint32(len(data)) > uint32(len(f.mem)) {
		return errors.New("unmapped")
	}
	copy(f.mem[addr:], data)
	return nil
}

func (f *fakeTarget) ReadRegisters(core int) ([]uint32, error) {
	if core < 0 || core > 1 {
		return nil, errors.New("bad core")
	}
	return append([]uint32(nil), f.regs[core]...), nil
}

func (f *fakeTarget) WriteRegister(core int, reg int, v uint32) error {
	if core < 0 || core > 1 || reg < 0 || reg >= RegisterCount {
		return errors.New("bad register")
	}
	f.regs[core][reg] = v
	return nil
}

func (f *fakeTarget) NumCores() int { return 2 }

func (f *fakeTarget) Stopped() (int, uint32, string, uint32, bool) {
	return f.core, f.pc, f.reason, 0, f.stopped
}

func (f *fakeTarget) Continue() { f.stopped = false }

func (f *fakeTarget) Step(core int) error {
	f.steps++
	f.core = core
	f.pc += 2
	f.stopped = true
	f.reason = "step"
	return nil
}

func (f *fakeTarget) Halt(core int) error {
	f.core = core
	f.stopped = true
	f.reason = "interrupt"
	return nil
}

func (f *fakeTarget) AddBreakpoint(addr uint32) error    { f.bp[addr] = true; return nil }
func (f *fakeTarget) RemoveBreakpoint(addr uint32) error { delete(f.bp, addr); return nil }
func (f *fakeTarget) AddWatchpoint(addr uint32, n int) error {
	f.watch[addr] = n
	return nil
}
func (f *fakeTarget) RemoveWatchpoint(addr uint32) error { delete(f.watch, addr); return nil }
func (f *fakeTarget) Reset() error                       { f.stopped = true; f.reason = "reset"; return nil }

func readReply(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	for {
		b, err := r.ReadByte()
		if err != nil {
			t.Fatal(err)
		}
		if b != '$' {
			continue
		}
		var payload []byte
		for {
			c, err := r.ReadByte()
			if err != nil {
				t.Fatal(err)
			}
			if c == '#' {
				break
			}
			payload = append(payload, c)
		}
		if _, err := r.ReadByte(); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ReadByte(); err != nil {
			t.Fatal(err)
		}
		return string(payload)
	}
}

func command(t *testing.T, st *connState, r *bufio.Reader, pkt string) (string, bool) {
	t.Helper()
	reply := make(chan string, 1)
	go func() { reply <- readReply(t, r) }()
	stop, err := st.command(pkt)
	if err != nil {
		t.Fatal(err)
	}
	return <-reply, stop
}

func TestRSPPackets(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	ft := newFakeTarget()
	st := &connState{conn: server, reader: bufio.NewReader(server), t: ft}
	r := bufio.NewReader(client)

	if got, _ := command(t, st, r, "qSupported"); !strings.Contains(got, "PacketSize=") {
		t.Fatalf("qSupported = %q", got)
	}
	if got, _ := command(t, st, r, "?"); got != "T05thread:1;" {
		t.Fatalf("? = %q", got)
	}
	copy(ft.mem[:], []byte{1, 2, 3, 4})
	if got, _ := command(t, st, r, "m0,4"); got != "01020304" {
		t.Fatalf("m = %q", got)
	}
	if got, _ := command(t, st, r, "M4,4:deadbeef"); got != "OK" {
		t.Fatalf("M = %q", got)
	}
	if got, _ := command(t, st, r, "m4,4"); got != "deadbeef" {
		t.Fatalf("m after M = %q", got)
	}
	if got, _ := command(t, st, r, "X8,1:}2"); got != "OK" {
		t.Fatalf("X = %q", got)
	}
	if got, _ := command(t, st, r, "m8,1"); got != "12" {
		t.Fatalf("m after X = %q", got)
	}

	g, _ := command(t, st, r, "g")
	if len(g) != RegisterCount*8 {
		t.Fatalf("g length = %d, want %d", len(g), RegisterCount*8)
	}
	if got, _ := command(t, st, r, "p19"); got != "00000001" {
		t.Fatalf("p19 = %q", got)
	}
	if got, _ := command(t, st, r, "P19=00000002"); got != "OK" {
		t.Fatalf("P19 = %q", got)
	}
	if ft.regs[0][16] != 0x02000000 {
		t.Fatalf("xpsr write = %#x", ft.regs[0][16])
	}

	if got, _ := command(t, st, r, "Hc2"); got != "OK" {
		t.Fatalf("Hc2 = %q", got)
	}
	if got, _ := command(t, st, r, "p0"); got != "01010000" {
		t.Fatalf("p0 on core 1 = %q", got)
	}
	if got, _ := command(t, st, r, "Hc1"); got != "OK" {
		t.Fatalf("Hc1 = %q", got)
	}

	if got, _ := command(t, st, r, "Z0,1000,2"); got != "OK" || !ft.bp[0x1000] {
		t.Fatalf("Z0 = %q", got)
	}
	if got, _ := command(t, st, r, "z0,1000,2"); got != "OK" || ft.bp[0x1000] {
		t.Fatalf("z0 = %q", got)
	}
	if got, _ := command(t, st, r, "Z2,2000,4"); got != "OK" || ft.watch[0x2000] != 4 {
		t.Fatalf("Z2 = %q", got)
	}

	if got, _ := command(t, st, r, "qfThreadInfo"); got != "m1,2" {
		t.Fatalf("qfThreadInfo = %q", got)
	}
	if got, _ := command(t, st, r, "qXfer:threads:read::0,100"); !strings.Contains(got, `id="2"`) {
		t.Fatalf("threads xfer = %q", got)
	}
	if got, _ := command(t, st, r, "qXfer:features:read:target.xml:0,1000"); !strings.Contains(got, "xpsr") {
		t.Fatalf("features xfer = %q", got)
	}

	if got, _ := command(t, st, r, "vCont;s:1"); !strings.HasPrefix(got, "T05") {
		t.Fatalf("step reply = %q", got)
	}
	if ft.steps != 1 || ft.pc != 0x1002 {
		t.Fatalf("step = %d pc=%#x", ft.steps, ft.pc)
	}

	if got, stop := command(t, st, r, "D"); got != "OK" || !stop {
		t.Fatalf("D = %q stop=%v", got, stop)
	}
}

func TestUnescapeBinary(t *testing.T) {
	got, err := unescapeBinary("ab}2\x04")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ab\x12\x04" {
		t.Fatalf("unescape = %q", got)
	}
}

func TestRegIndex(t *testing.T) {
	if i, ok := regIndex(25); !ok || i != 16 {
		t.Fatalf("regIndex(25) = %d,%v", i, ok)
	}
	if _, ok := regIndex(16); ok {
		t.Fatal("regIndex(16) should not be valid")
	}
}

func TestXferChunks(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	st := &connState{conn: server, reader: bufio.NewReader(server), t: newFakeTarget()}
	r := bufio.NewReader(client)
	data := strings.Repeat("x", 10)
	go st.xfer("qXfer:threads:read::0,4", []byte(data))
	if got := readReply(t, r); got != "mxxxx" {
		t.Fatalf("first chunk = %q", got)
	}
	go st.xfer("qXfer:threads:read::8,4", []byte(data))
	if got := readReply(t, r); got != "lxx" {
		t.Fatalf("last chunk = %q", got)
	}
}
