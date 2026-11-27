package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// logicUARTEvent is one byte written to a hardware UART.
type logicUARTEvent struct {
	bus   int
	cycle uint64
	data  byte
}

type logicSPIEvent struct {
	bus   int
	cycle uint64
	tx    byte
	rx    byte
}

type logicI2CEvent struct {
	bus   int
	cycle uint64
	kind  string
	addr  uint8
	data  byte
	ack   bool
}

type logicUARTSpec struct {
	tx, rx, baud int
}

type logicI2CSpec struct {
	sda, scl int
}

type logicSPISpec struct {
	sck, mosi, miso, cs int
}

// logicCapture owns the headless logic analyzer: a pin trace plus the
// controller-level UART/SPI/I2C taps and optional pin-level protocol decoders.
type logicCapture struct {
	board    string
	pins     []int
	interval uint64
	depth    int
	trace    *devices.Trace

	uartSpecs []logicUARTSpec
	i2cSpecs  []logicI2CSpec
	spiSpecs  []logicSPISpec

	uart []logicUARTEvent
	spi  []logicSPIEvent
	i2c  []logicI2CEvent

	samples int
}

func parsePinList(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []int
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty GPIO in pin list")
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 47 {
			return nil, fmt.Errorf("invalid GPIO %q", part)
		}
		out = append(out, n)
	}
	return out, nil
}

func validateLogicPin(pin int, what string, optional bool) error {
	if pin == -1 && optional {
		return nil
	}
	if pin < 0 || pin > 47 {
		return fmt.Errorf("%s pin GP%d is not a valid GPIO", what, pin)
	}
	return nil
}

func parseIntFields(s string, what string) ([]int, error) {
	var out []int
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("%s expects integers: %q", what, s)
		}
		out = append(out, n)
	}
	return out, nil
}

// newLogicCapture parses the analyzer flags and prepares the pin set. It does
// not attach to the chip yet.
func newLogicCapture(pinsStr, uartStr, i2cStr, spiStr string, rate float64, depth int, board string) (*logicCapture, error) {
	if depth <= 0 {
		return nil, fmt.Errorf("logic depth must be positive")
	}
	if rate <= 0 {
		return nil, fmt.Errorf("logic rate must be positive")
	}
	if rate > float64(rp2350.SysClkHz) {
		return nil, fmt.Errorf("logic rate must be at most %g Hz", float64(rp2350.SysClkHz))
	}
	l := &logicCapture{board: board, depth: depth, interval: uint64(float64(rp2350.SysClkHz) / rate)}
	if l.interval == 0 {
		l.interval = 1
	}
	seen := map[int]bool{}
	add := func(p int) {
		if p >= 0 && !seen[p] {
			seen[p] = true
			l.pins = append(l.pins, p)
		}
	}
	pins, err := parsePinList(pinsStr)
	if err != nil {
		return nil, err
	}
	for _, p := range pins {
		add(p)
	}
	if uartStr != "" {
		f, err := parseIntFields(uartStr, "-logic-uart")
		if err != nil {
			return nil, err
		}
		var s logicUARTSpec
		switch len(f) {
		case 2:
			s = logicUARTSpec{tx: f[0], rx: f[0], baud: f[1]}
		case 3:
			s = logicUARTSpec{tx: f[0], rx: f[1], baud: f[2]}
		default:
			return nil, fmt.Errorf("-logic-uart expects tx,baud or tx,rx,baud")
		}
		if s.baud <= 0 {
			return nil, fmt.Errorf("-logic-uart baud must be positive")
		}
		if err := validateLogicPin(s.tx, "-logic-uart tx", false); err != nil {
			return nil, err
		}
		if err := validateLogicPin(s.rx, "-logic-uart rx", false); err != nil {
			return nil, err
		}
		l.uartSpecs = append(l.uartSpecs, s)
		add(s.tx)
		add(s.rx)
	}
	if i2cStr != "" {
		f, err := parseIntFields(i2cStr, "-logic-i2c")
		if err != nil || len(f) != 2 {
			return nil, fmt.Errorf("-logic-i2c expects sda,scl")
		}
		if err := validateLogicPin(f[0], "-logic-i2c sda", false); err != nil {
			return nil, err
		}
		if err := validateLogicPin(f[1], "-logic-i2c scl", false); err != nil {
			return nil, err
		}
		l.i2cSpecs = append(l.i2cSpecs, logicI2CSpec{sda: f[0], scl: f[1]})
		add(f[0])
		add(f[1])
	}
	if spiStr != "" {
		f, err := parseIntFields(spiStr, "-logic-spi")
		if err != nil || len(f) < 2 || len(f) > 4 {
			return nil, fmt.Errorf("-logic-spi expects sck,mosi[,miso[,cs]]")
		}
		s := logicSPISpec{sck: f[0], mosi: f[1], miso: -1, cs: -1}
		if len(f) > 2 {
			s.miso = f[2]
		}
		if len(f) > 3 {
			s.cs = f[3]
		}
		if err := validateLogicPin(s.sck, "-logic-spi sck", false); err != nil {
			return nil, err
		}
		if err := validateLogicPin(s.mosi, "-logic-spi mosi", false); err != nil {
			return nil, err
		}
		if err := validateLogicPin(s.miso, "-logic-spi miso", true); err != nil {
			return nil, err
		}
		if err := validateLogicPin(s.cs, "-logic-spi cs", true); err != nil {
			return nil, err
		}
		l.spiSpecs = append(l.spiSpecs, s)
		add(s.sck)
		add(s.mosi)
		if s.miso >= 0 {
			add(s.miso)
		}
		if s.cs >= 0 {
			add(s.cs)
		}
	}
	if len(l.pins) > devices.MaxTraceChannels {
		return nil, fmt.Errorf("logic capture supports at most %d pins, got %d", devices.MaxTraceChannels, len(l.pins))
	}
	return l, nil
}

// attach installs the trace and wraps the controller taps. Call it after the
// runner has set its own callbacks.
func (l *logicCapture) attach(c *rp2350.Chip) {
	if len(l.pins) > 0 {
		l.trace = devices.NewTrace(c, l.pins, l.interval, l.depth)
		// Protocol capture only needs the digital wire level.
		l.trace.Digital = true
		// Sample the attach instant: the capture window starts here, and the
		// periodic grid then continues on the trace's own interval.
		l.trace.Tick()
		c.AddPeriodicHook(l.trace.Interval, l.trace.Tick)
	}
	prevU0, prevU1 := c.OnUART0, c.OnUART1
	c.OnUART0 = func(b byte) {
		if prevU0 != nil {
			prevU0(b)
		}
		l.addUART(0, c.Cycles, b)
	}
	c.OnUART1 = func(b byte) {
		if prevU1 != nil {
			prevU1(b)
		}
		l.addUART(1, c.Cycles, b)
	}
	prevS0, prevS1 := c.OnSPI0, c.OnSPI1
	c.OnSPI0 = func(tx byte) byte {
		rx := byte(0xFF)
		if prevS0 != nil {
			rx = prevS0(tx)
		}
		l.addSPI(0, c.Cycles, tx, rx)
		return rx
	}
	c.OnSPI1 = func(tx byte) byte {
		rx := byte(0xFF)
		if prevS1 != nil {
			rx = prevS1(tx)
		}
		l.addSPI(1, c.Cycles, tx, rx)
		return rx
	}
	prevI2C := c.OnI2C
	c.OnI2C = func(bus int, cycles uint64, kind string, addr uint8, data byte, ack bool) {
		if prevI2C != nil {
			prevI2C(bus, cycles, kind, addr, data, ack)
		}
		l.addI2C(bus, cycles, kind, addr, data, ack)
	}
}

const logicEventLimit = 4096

func (l *logicCapture) addUART(bus int, cycle uint64, b byte) {
	l.uart = append(l.uart, logicUARTEvent{bus: bus, cycle: cycle, data: b})
	if len(l.uart) > logicEventLimit {
		l.uart = l.uart[len(l.uart)-logicEventLimit/2:]
	}
}

func (l *logicCapture) addSPI(bus int, cycle uint64, tx, rx byte) {
	l.spi = append(l.spi, logicSPIEvent{bus: bus, cycle: cycle, tx: tx, rx: rx})
	if len(l.spi) > logicEventLimit {
		l.spi = l.spi[len(l.spi)-logicEventLimit/2:]
	}
}

func (l *logicCapture) addI2C(bus int, cycle uint64, kind string, addr uint8, data byte, ack bool) {
	l.i2c = append(l.i2c, logicI2CEvent{bus: bus, cycle: cycle, kind: kind, addr: addr, data: data, ack: ack})
	if len(l.i2c) > logicEventLimit {
		l.i2c = l.i2c[len(l.i2c)-logicEventLimit/2:]
	}
}

func channelOf(pins []int, pin int) int {
	for i, p := range pins {
		if p == pin {
			return i
		}
	}
	return -1
}

func (l *logicCapture) uartTapText() string {
	if len(l.uart) == 0 {
		return ""
	}
	base := l.uart[0].cycle
	var b strings.Builder
	for _, ev := range l.uart {
		fmt.Fprintf(&b, "%8.3fms UART%d TX 0x%02x %q\n",
			float64(ev.cycle-base)/150e3, ev.bus, ev.data, string(rune(ev.data)))
	}
	return b.String()
}

func (l *logicCapture) spiTapText() string {
	if len(l.spi) == 0 {
		return ""
	}
	base := l.spi[0].cycle
	var b strings.Builder
	for _, ev := range l.spi {
		fmt.Fprintf(&b, "%8.3fms SPI%d MOSI 0x%02x MISO 0x%02x\n",
			float64(ev.cycle-base)/150e3, ev.bus, ev.tx, ev.rx)
	}
	return b.String()
}

func (l *logicCapture) i2cTapText() string {
	if len(l.i2c) == 0 {
		return ""
	}
	base := l.i2c[0].cycle
	var b strings.Builder
	for _, ev := range l.i2c {
		fmt.Fprintf(&b, "%8.3fms I2C%d %s", float64(ev.cycle-base)/150e3, ev.bus, ev.kind)
		switch ev.kind {
		case "addr":
			fmt.Fprintf(&b, " 0x%02x", ev.addr)
		case "data", "read":
			fmt.Fprintf(&b, " 0x%02x", ev.data)
		}
		if ev.kind == "data" || ev.kind == "read" {
			if ev.ack {
				b.WriteString(" ack")
			} else {
				b.WriteString(" nack")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (l *logicCapture) pinSections(samples []devices.TraceSample) []devices.LogicSection {
	rate := 150e6 / float64(l.interval)
	var sections []devices.LogicSection
	for _, s := range l.uartSpecs {
		ch := channelOf(l.pins, s.tx)
		if ch < 0 {
			continue
		}
		frames := devices.DecodeUART(samples, ch, float64(s.baud), rate)
		var b strings.Builder
		for _, f := range frames {
			if f.Data >= 0x20 && f.Data < 0x7f {
				fmt.Fprintf(&b, "%8.3fms 0x%02x %q\n", float64(f.T-samples[0].T)/150e3, f.Data, string(rune(f.Data)))
			} else {
				fmt.Fprintf(&b, "%8.3fms 0x%02x\n", float64(f.T-samples[0].T)/150e3, f.Data)
			}
		}
		if b.Len() == 0 {
			b.WriteString("no frames\n")
		}
		sections = append(sections, devices.LogicSection{
			Name:   fmt.Sprintf("UART pins GP%d", s.tx),
			Detail: fmt.Sprintf("%d baud", s.baud),
			Text:   b.String(),
		})
	}
	for _, s := range l.i2cSpecs {
		sda, scl := channelOf(l.pins, s.sda), channelOf(l.pins, s.scl)
		if sda < 0 || scl < 0 {
			continue
		}
		events := devices.DecodeI2C(samples, sda, scl)
		var b strings.Builder
		for _, ev := range events {
			switch ev.Kind {
			case devices.I2CStart:
				b.WriteString("S")
			case devices.I2CAddr:
				rw := "W"
				if ev.Read {
					rw = "R"
				}
				fmt.Fprintf(&b, " %02x%s", ev.Byte>>1, rw)
			case devices.I2CData:
				fmt.Fprintf(&b, " %02x", ev.Byte)
			case devices.I2CAck:
				if ev.Ack {
					b.WriteString(" A")
				} else {
					b.WriteString(" N")
				}
			case devices.I2CStop:
				b.WriteString(" P\n")
			}
		}
		if b.Len() == 0 {
			b.WriteString("no transactions\n")
		}
		sections = append(sections, devices.LogicSection{
			Name: fmt.Sprintf("I2C pins SDA GP%d SCL GP%d", s.sda, s.scl),
			Text: b.String(),
		})
	}
	for _, s := range l.spiSpecs {
		sck, mosi := channelOf(l.pins, s.sck), channelOf(l.pins, s.mosi)
		if sck < 0 || mosi < 0 {
			continue
		}
		frames := devices.DecodeSPI(samples, sck, mosi, channelOf(l.pins, s.miso), channelOf(l.pins, s.cs))
		var b strings.Builder
		for _, f := range frames {
			fmt.Fprintf(&b, "%8.3fms MOSI 0x%02x MISO 0x%02x\n", float64(f.T-samples[0].T)/150e3, f.MOSI, f.MISO)
		}
		if b.Len() == 0 {
			b.WriteString("no frames\n")
		}
		sections = append(sections, devices.LogicSection{
			Name: fmt.Sprintf("SPI pins SCK GP%d MOSI GP%d", s.sck, s.mosi),
			Text: b.String(),
		})
	}
	return sections
}

// write saves the trace plus taps and returns the path written.
// writeVCD exports the captured pin trace for GTKWave or PulseView.
func (l *logicCapture) writeVCD(path string) (string, error) {
	if l.trace == nil {
		return path, fmt.Errorf("no pin trace captured")
	}
	pins, samples := l.trace.Snapshot()
	f, err := os.Create(path)
	if err != nil {
		return path, err
	}
	if err := devices.WriteVCD(f, pins, samples, l.interval); err != nil {
		_ = f.Close()
		return path, err
	}
	if err := f.Close(); err != nil {
		return path, err
	}
	abs, _ := filepath.Abs(path)
	return abs, nil
}

func (l *logicCapture) write(path string) (string, error) {
	var pins []int
	var samples []devices.TraceSample
	var interval uint64
	if l.trace != nil {
		pins, samples = l.trace.Snapshot()
		interval = l.interval
		l.samples = len(samples)
	}
	trace := devices.LogicTrace{
		BoardID:  l.board,
		Captured: time.Now(),
		Interval: interval,
		Pins:     pins,
		Samples:  samples,
	}
	if text := l.uartTapText(); text != "" {
		trace.Sections = append(trace.Sections, devices.LogicSection{Name: "UART taps", Text: text})
	}
	if text := l.spiTapText(); text != "" {
		trace.Sections = append(trace.Sections, devices.LogicSection{Name: "SPI taps", Text: text})
	}
	if text := l.i2cTapText(); text != "" {
		trace.Sections = append(trace.Sections, devices.LogicSection{Name: "I2C taps", Text: text})
	}
	if len(samples) > 0 {
		trace.Sections = append(trace.Sections, l.pinSections(samples)...)
	}
	if path == "" {
		path = devices.LogicTraceFilename(l.board, time.Now())
	}
	f, err := os.Create(path)
	if err != nil {
		return path, err
	}
	if err := devices.WriteLogicTrace(f, trace); err != nil {
		_ = f.Close()
		return path, err
	}
	if err := f.Close(); err != nil {
		return path, err
	}
	return filepath.Abs(path)
}
