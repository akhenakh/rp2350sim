// Package gdb implements the GDB Remote Serial Protocol (RSP) server used by
// the headless runner and the desktop UI. The emulator-specific side is
// supplied through Target, so the protocol code is independent of the chip.
package gdb

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Target is the machine the RSP server debugs. All methods may be called from
// the server goroutine; implementations must serialize access with their own
// run loop (the CLI and UI adapters lock the chip).
type Target interface {
	ReadMemory(addr uint32, size int) ([]byte, error)
	WriteMemory(addr uint32, data []byte) error

	// ReadRegisters returns the 17 architecturally exposed registers in the
	// order r0-r12, sp, lr, pc, xpsr.
	ReadRegisters(core int) ([]uint32, error)
	WriteRegister(core int, reg int, value uint32) error

	NumCores() int
	// Stopped reports the current stop. ok is false while the target runs.
	Stopped() (core int, pc uint32, reason string, addr uint32, ok bool)
	Continue()
	Step(core int) error
	Halt(core int) error
	AddBreakpoint(addr uint32) error
	RemoveBreakpoint(addr uint32) error
	AddWatchpoint(addr uint32, size int) error
	RemoveWatchpoint(addr uint32) error
	Reset() error
}

// RegisterCount is the number of registers in the target description. The
// ARM M-profile layout keeps xpsr at GDB register number 25, but the g/G
// packets carry only the defined registers (r0-r12, sp, lr, pc, xpsr).
const RegisterCount = 17

const targetXML = `<?xml version="1.0"?>
<!DOCTYPE target SYSTEM "gdb-target.dtd">
<target>
  <architecture>arm</architecture>
  <feature name="org.gnu.gdb.arm.m-profile">
    <reg name="r0" bitsize="32" regnum="0"/>
    <reg name="r1" bitsize="32" regnum="1"/>
    <reg name="r2" bitsize="32" regnum="2"/>
    <reg name="r3" bitsize="32" regnum="3"/>
    <reg name="r4" bitsize="32" regnum="4"/>
    <reg name="r5" bitsize="32" regnum="5"/>
    <reg name="r6" bitsize="32" regnum="6"/>
    <reg name="r7" bitsize="32" regnum="7"/>
    <reg name="r8" bitsize="32" regnum="8"/>
    <reg name="r9" bitsize="32" regnum="9"/>
    <reg name="r10" bitsize="32" regnum="10"/>
    <reg name="r11" bitsize="32" regnum="11"/>
    <reg name="r12" bitsize="32" regnum="12"/>
    <reg name="sp" bitsize="32" type="data_ptr" regnum="13"/>
    <reg name="lr" bitsize="32" regnum="14"/>
    <reg name="pc" bitsize="32" type="code_ptr" regnum="15"/>
    <reg name="xpsr" bitsize="32" regnum="25"/>
  </feature>
</target>
`

func threadsXML(cores int) string {
	var b strings.Builder
	b.WriteString("<threads>")
	for i := range cores {
		fmt.Fprintf(&b, `<thread id="%d" core="%d" name="core%d"/>`, i+1, i, i)
	}
	b.WriteString("</threads>")
	return b.String()
}

// Server serves one GDB client at a time on a listener.
type Server struct {
	t    Target
	logf func(format string, args ...any)

	mu     sync.Mutex
	ln     net.Listener
	conn   net.Conn
	done   chan struct{}
	closed bool
}

// NewServer creates a server for t. logf may be nil.
func NewServer(t Target, logf func(format string, args ...any)) *Server {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Server{t: t, logf: logf, done: make(chan struct{})}
}

// Serve accepts clients until the listener is closed. It starts the target
// halted so the first GDB prompt shows the current machine state.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = ln.Close()
		return nil
	}
	if s.ln != nil {
		s.mu.Unlock()
		return errors.New("gdb: server already serving")
	}
	s.ln = ln
	s.mu.Unlock()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return nil
			default:
				return err
			}
		}
		s.logf("gdb client connected", "remote", conn.RemoteAddr().String())
		s.mu.Lock()
		s.conn = conn
		s.mu.Unlock()
		s.handle(conn)
		s.mu.Lock()
		s.conn = nil
		s.mu.Unlock()
		s.logf("gdb client disconnected", "remote", conn.RemoteAddr().String())
	}
}

// Close stops the listener and disconnects an active client.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	ln := s.ln
	conn := s.conn
	close(s.done)
	s.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if ln != nil {
		return ln.Close()
	}
	return nil
}

func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	defer s.t.Continue() // never leave the machine frozen after a disconnect

	st := &connState{conn: conn, t: s.t, logf: s.logf, reader: bufio.NewReader(conn)}
	// GDB's "target remote" stops the target on attach.
	core := 0
	if c, _, _, _, ok := s.t.Stopped(); ok {
		core = c
	}
	_ = s.t.Halt(core)
	st.sel.Store(int32(core))

	for {
		pkt, err := st.readPacket()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.logf("gdb read", "err", err)
			}
			return
		}
		if pkt == "" {
			continue
		}
		stop, err := st.command(pkt)
		if err != nil {
			st.send("E" + err.Error())
			continue
		}
		if stop {
			return
		}
	}
}

type connState struct {
	conn   net.Conn
	reader *bufio.Reader
	t      Target
	logf   func(format string, args ...any)

	writeMu sync.Mutex
	// runMu guards running/gen. A continue starts one waiter goroutine which
	// sends the stop reply; the reader stays free for the 0x03 interrupt.
	runMu   sync.Mutex
	running bool
	gen     uint64

	// sel is the selected thread for step/registers (Hc/Hg), 0-based. The
	// stop waiter updates it while the packet reader may be handling Ctrl-C,
	// so it is atomic rather than plain int.
	sel atomic.Int32
}

func (st *connState) send(payload string) {
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	var sum byte
	for i := range len(payload) {
		sum += payload[i]
	}
	_, _ = io.WriteString(st.conn, "$"+payload+"#"+fmt.Sprintf("%02x", sum))
}

func (st *connState) ack() {
	st.writeMu.Lock()
	_, _ = io.WriteString(st.conn, "+")
	st.writeMu.Unlock()
}

func (st *connState) readPacket() (string, error) {
	r := st.reader
	for {
		b, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		switch b {
		case '+', '-':
			continue
		case 0x03: // Ctrl-C: asynchronous interrupt
			st.logf("gdb interrupt")
			st.runMu.Lock()
			running := st.running
			st.runMu.Unlock()
			if running {
				_ = st.t.Halt(int(st.sel.Load()))
			}
			continue
		case '$':
			var payload []byte
			var sum byte
			for {
				c, err := r.ReadByte()
				if err != nil {
					return "", err
				}
				if c == '#' {
					break
				}
				payload = append(payload, c)
				sum += c
			}
			c1, err := r.ReadByte()
			if err != nil {
				return "", err
			}
			c2, err := r.ReadByte()
			if err != nil {
				return "", err
			}
			want, err := strconv.ParseUint(string([]byte{c1, c2}), 16, 8)
			if err != nil || byte(want) != sum {
				st.writeMu.Lock()
				_, _ = io.WriteString(st.conn, "-")
				st.writeMu.Unlock()
				continue
			}
			st.ack()
			return string(payload), nil
		}
	}
}

// command handles one packet. stop is true for detach/kill, which closes the
// connection after the reply.
func (st *connState) command(pkt string) (stop bool, err error) {
	switch {
	case pkt == "?":
		st.send(st.stopReply())
	case strings.HasPrefix(pkt, "qSupported"):
		st.send("PacketSize=4000;qXfer:features:read+;qXfer:threads:read+;vContSupported+;swbreak+;hwbreak+;qXfer:memory-map:read-")
	case pkt == "qAttached":
		st.send("1")
	case strings.HasPrefix(pkt, "qOffsets"):
		st.send("Text=0;Data=0;Bss=0")
	case pkt == "qC":
		st.send(fmt.Sprintf("QC%d", st.sel.Load()+1))
	case pkt == "qfThreadInfo":
		st.send(st.threadList())
	case pkt == "qsThreadInfo":
		st.send("l")
	case strings.HasPrefix(pkt, "qThreadExtraInfo,"):
		st.threadExtraInfo(pkt)
	case strings.HasPrefix(pkt, "qXfer:features:read:"):
		st.xfer(pkt, []byte(targetXML))
	case strings.HasPrefix(pkt, "qXfer:threads:read:"):
		st.xfer(pkt, []byte(threadsXML(st.t.NumCores())))
	case strings.HasPrefix(pkt, "qXfer:"):
		st.send("") // unsupported annex
	case strings.HasPrefix(pkt, "qSymbol:"):
		st.send("OK")
	case pkt == "qTStatus":
		st.send("")
	case pkt == "!":
		st.send("OK")
	case strings.HasPrefix(pkt, "Hc"), strings.HasPrefix(pkt, "Hg"):
		st.handleThreadSelect(pkt)
	case strings.HasPrefix(pkt, "T"):
		st.send("OK")
	case strings.HasPrefix(pkt, "vCont?"):
		st.send("vCont;c;C;s;S;t")
	case strings.HasPrefix(pkt, "vCont;"):
		st.vCont(pkt)
	case pkt == "vCtrlC":
		_ = st.t.Halt(int(st.sel.Load()))
		st.setRunning(false)
		st.send(st.stopReply())
	case pkt == "c" || strings.HasPrefix(pkt, "C"):
		st.startRun()
	case pkt == "s" || strings.HasPrefix(pkt, "S"):
		if err := st.t.Step(int(st.sel.Load())); err != nil {
			st.send("E" + err.Error())
			break
		}
		st.setRunning(false)
		st.send(st.stopReply())
	case strings.HasPrefix(pkt, "Z"), strings.HasPrefix(pkt, "z"):
		st.breakpoint(pkt)
	case strings.HasPrefix(pkt, "m"):
		st.readMemory(pkt)
	case strings.HasPrefix(pkt, "M"):
		st.writeMemory(pkt, false)
	case strings.HasPrefix(pkt, "X"):
		st.writeMemory(pkt, true)
	case strings.HasPrefix(pkt, "g"):
		st.readRegisters()
	case strings.HasPrefix(pkt, "G"):
		st.writeRegisters(pkt)
	case strings.HasPrefix(pkt, "p"):
		st.readRegister(pkt)
	case strings.HasPrefix(pkt, "P"):
		st.writeRegister(pkt)
	case strings.HasPrefix(pkt, "qRcmd,"):
		st.remoteCommand(pkt)
	case pkt == "D":
		st.setRunning(false)
		st.t.Continue()
		st.send("OK")
		return true, nil
	case pkt == "k":
		st.t.Continue()
		st.send("OK")
		return true, nil
	case pkt == "R":
		st.send("OK")
	default:
		st.send("")
	}
	return false, nil
}

func (st *connState) stopReply() string {
	core, _, reason, addr, ok := st.t.Stopped()
	if !ok {
		return "T05thread:1;"
	}
	tid := core + 1
	switch reason {
	case "paused", "interrupt":
		return fmt.Sprintf("T02thread:%d;", tid)
	case "watchpoint":
		return fmt.Sprintf("T05watch:%x;thread:%d;", addr, tid)
	default:
		return fmt.Sprintf("T05thread:%d;", tid)
	}
}

func (st *connState) threadList() string {
	n := st.t.NumCores()
	if n <= 0 {
		return "l"
	}
	var b strings.Builder
	b.WriteString("m")
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%d", i+1)
	}
	return b.String()
}

func (st *connState) threadExtraInfo(pkt string) {
	id, err := strconv.ParseInt(strings.TrimPrefix(pkt, "qThreadExtraInfo,"), 16, 32)
	if err != nil || id < 1 || int(id) > st.t.NumCores() {
		st.send("E22")
		return
	}
	name := fmt.Sprintf("core%d", id-1)
	st.send(hex.EncodeToString([]byte(name)))
}

func (st *connState) handleThreadSelect(pkt string) {
	// Hc-1 / Hg0 select all; Hc1/Hg1 select core 0.
	arg := pkt[2:]
	if arg == "-1" || arg == "0" {
		st.sel.Store(0)
		st.send("OK")
		return
	}
	id, err := strconv.ParseInt(arg, 16, 32)
	if err != nil || id < 1 || int(id) > st.t.NumCores() {
		st.send("E22")
		return
	}
	st.sel.Store(int32(id) - 1)
	st.send("OK")
}

func (st *connState) vCont(pkt string) {
	for op := range strings.SplitSeq(strings.TrimPrefix(pkt, "vCont;"), ";") {
		if op == "" {
			continue
		}
		kind := op[0]
		core := int(st.sel.Load())
		if _, thread, ok := strings.Cut(op, ":"); ok {
			id, err := strconv.ParseInt(thread, 16, 32)
			if err == nil && id >= 1 && int(id) <= st.t.NumCores() {
				core = int(id) - 1
			}
		}
		switch kind {
		case 'c', 'C':
			st.startRun()
			return
		case 't':
			_ = st.t.Halt(core)
			st.setRunning(false)
			st.send(st.stopReply())
			return
		case 's', 'S':
			if err := st.t.Step(core); err != nil {
				st.send("E" + err.Error())
				return
			}
			st.setRunning(false)
			st.send(st.stopReply())
			return
		}
	}
	st.send("")
}

func (st *connState) setRunning(v bool) {
	st.runMu.Lock()
	st.running = v
	st.runMu.Unlock()
}

func (st *connState) startRun() {
	st.runMu.Lock()
	if st.running {
		st.runMu.Unlock()
		return
	}
	st.running = true
	st.gen++
	gen := st.gen
	st.runMu.Unlock()
	st.t.Continue()
	go st.waitStop(gen)
}

func (st *connState) waitStop(gen uint64) {
	for {
		core, _, _, _, ok := st.t.Stopped()
		if ok {
			st.runMu.Lock()
			if st.gen != gen {
				st.runMu.Unlock()
				return
			}
			st.running = false
			st.runMu.Unlock()
			st.sel.Store(int32(core))
			st.logf("gdb stopped", "core", core)
			st.send(st.stopReply())
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (st *connState) breakpoint(pkt string) {
	add := pkt[0] == 'Z'
	rest := pkt[1:]
	kindEnd := strings.IndexByte(rest, ',')
	if kindEnd != 1 {
		st.send("E22")
		return
	}
	kind := rest[0] - '0'
	rest = rest[2:]
	parts := strings.SplitN(rest, ",", 2)
	addr64, err := strconv.ParseUint(parts[0], 16, 32)
	if err != nil {
		st.send("E22")
		return
	}
	addr := uint32(addr64)
	size := 4
	if len(parts) == 2 {
		if n, err := strconv.ParseUint(parts[1], 16, 32); err == nil && n > 0 {
			size = int(n)
		}
	}
	switch kind {
	case 0, 1: // software / hardware execution breakpoint
		if add {
			err = st.t.AddBreakpoint(addr)
		} else {
			err = st.t.RemoveBreakpoint(addr)
		}
	case 2, 3, 4: // write / read / access watchpoint
		if add {
			err = st.t.AddWatchpoint(addr, size)
		} else {
			err = st.t.RemoveWatchpoint(addr)
		}
	default:
		st.send("")
		return
	}
	if err != nil {
		st.send("E" + err.Error())
		return
	}
	st.send("OK")
}

func (st *connState) readMemory(pkt string) {
	rest := pkt[1:]
	parts := strings.SplitN(rest, ",", 2)
	if len(parts) != 2 {
		st.send("E22")
		return
	}
	addr, err1 := strconv.ParseUint(parts[0], 16, 32)
	size, err2 := strconv.ParseUint(parts[1], 16, 32)
	if err1 != nil || err2 != nil || size > 1<<16 {
		st.send("E22")
		return
	}
	data, err := st.t.ReadMemory(uint32(addr), int(size))
	if err != nil {
		st.send("E14")
		return
	}
	st.send(hex.EncodeToString(data))
}

func (st *connState) writeMemory(pkt string, binary bool) {
	rest := pkt[1:]
	addrLen, encoded, ok := strings.Cut(rest, ":")
	if !ok {
		st.send("E22")
		return
	}
	ap := strings.SplitN(addrLen, ",", 2)
	if len(ap) != 2 {
		st.send("E22")
		return
	}
	addr, err := strconv.ParseUint(ap[0], 16, 32)
	if err != nil {
		st.send("E22")
		return
	}
	if _, err := strconv.ParseUint(ap[1], 16, 32); err != nil {
		st.send("E22")
		return
	}
	var data []byte
	if binary {
		data, err = unescapeBinary(encoded)
	} else {
		data, err = hex.DecodeString(encoded)
	}
	if err != nil {
		st.send("E22")
		return
	}
	if len(data) > 1<<16 {
		st.send("E22")
		return
	}
	if err := st.t.WriteMemory(uint32(addr), data); err != nil {
		st.send("E14")
		return
	}
	st.send("OK")
}

func unescapeBinary(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))
	esc := false
	for i := range len(s) {
		c := s[i]
		if esc {
			out = append(out, c^0x20)
			esc = false
			continue
		}
		if c == '}' {
			esc = true
			continue
		}
		out = append(out, c)
	}
	if esc {
		return nil, errors.New("truncated escape")
	}
	return out, nil
}

func (st *connState) readRegisters() {
	regs, err := st.t.ReadRegisters(int(st.sel.Load()))
	if err != nil {
		st.send("E14")
		return
	}
	var b strings.Builder
	for _, v := range regs {
		b.WriteString(hex32(v))
	}
	st.send(b.String())
}

func (st *connState) writeRegisters(pkt string) {
	data, err := hex.DecodeString(pkt[1:])
	if err != nil || len(data) != RegisterCount*4 {
		st.send("E22")
		return
	}
	for i := range RegisterCount {
		v := uint32(data[i*4]) | uint32(data[i*4+1])<<8 | uint32(data[i*4+2])<<16 | uint32(data[i*4+3])<<24
		if err := st.t.WriteRegister(int(st.sel.Load()), i, v); err != nil {
			st.send("E" + err.Error())
			return
		}
	}
	st.send("OK")
}

func (st *connState) readRegister(pkt string) {
	n, err := strconv.ParseUint(pkt[1:], 16, 32)
	if err != nil {
		st.send("E22")
		return
	}
	idx, ok := regIndex(int(n))
	if !ok {
		st.send("E22")
		return
	}
	regs, err := st.t.ReadRegisters(int(st.sel.Load()))
	if err != nil || idx >= len(regs) {
		st.send("E14")
		return
	}
	st.send(hex32(regs[idx]))
}

func (st *connState) writeRegister(pkt string) {
	rest := strings.TrimPrefix(pkt, "P")
	parts := strings.SplitN(rest, "=", 2)
	if len(parts) != 2 {
		st.send("E22")
		return
	}
	n, err := strconv.ParseUint(parts[0], 16, 32)
	if err != nil {
		st.send("E22")
		return
	}
	idx, ok := regIndex(int(n))
	if !ok {
		st.send("E22")
		return
	}
	data, err := hex.DecodeString(parts[1])
	if err != nil || len(data) != 4 {
		st.send("E22")
		return
	}
	v := uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24
	if err := st.t.WriteRegister(int(st.sel.Load()), idx, v); err != nil {
		st.send("E" + err.Error())
		return
	}
	st.send("OK")
}

// regIndex maps a GDB register number to the packed g/G order. The ARM
// M-profile xpsr is register 25 in the target description but slot 16 here.
func regIndex(n int) (int, bool) {
	switch {
	case n >= 0 && n <= 15:
		return n, true
	case n == 25:
		return 16, true
	}
	return 0, false
}

func (st *connState) remoteCommand(pkt string) {
	data, err := hex.DecodeString(strings.TrimPrefix(pkt, "qRcmd,"))
	if err != nil {
		st.send("E22")
		return
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		st.send("")
		return
	}
	switch fields[0] {
	case "reset":
		if err := st.t.Reset(); err != nil {
			st.send("E" + err.Error())
			return
		}
		st.send("OK")
	case "halt":
		if err := st.t.Halt(int(st.sel.Load())); err != nil {
			st.send("E" + err.Error())
			return
		}
		st.send("OK")
	default:
		st.send("")
	}
}

func (st *connState) xfer(pkt string, data []byte) {
	// qXfer:<object>:read:<annex>:<offset>,<length>
	rest := strings.TrimPrefix(pkt, "qXfer:")
	object, rest, ok := strings.Cut(rest, ":read:")
	if !ok {
		st.send("")
		return
	}
	_ = object
	// rest is "<annex>:<offset>,<length>" (annex may be empty).
	i := strings.LastIndexByte(rest, ':')
	if i < 0 {
		st.send("E22")
		return
	}
	rangeStr := rest[i+1:]
	parts := strings.SplitN(rangeStr, ",", 2)
	if len(parts) != 2 {
		st.send("E22")
		return
	}
	off, err1 := strconv.ParseUint(parts[0], 16, 32)
	length, err2 := strconv.ParseUint(parts[1], 16, 32)
	if err1 != nil || err2 != nil {
		st.send("E22")
		return
	}
	if off >= uint64(len(data)) || length == 0 {
		st.send("l")
		return
	}
	end := min(off+length, uint64(len(data)))
	prefix := "m"
	if end == uint64(len(data)) {
		prefix = "l"
	}
	st.send(prefix + string(data[off:end]))
}

func hex32(v uint32) string {
	var b [4]byte
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
	return hex.EncodeToString(b[:])
}
