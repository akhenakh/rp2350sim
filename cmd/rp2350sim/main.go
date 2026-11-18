// Command rp2350sim runs RP2350 firmware headlessly.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/firmware"
	"github.com/akhenakh/rp2350sim/internal/m33"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// stringList collects repeatable string flags such as -expect.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// expectSet matches console output against the requested strings while also
// forwarding it to stdout.
type expectSet struct {
	wants []string
	hit   []bool
	buf   []byte
	all   bool
}

func (e *expectSet) add(w string) {
	if w == "" {
		return
	}
	e.wants = append(e.wants, w)
	e.hit = append(e.hit, false)
}

func (e *expectSet) console(b []byte) {
	os.Stdout.Write(b)
	if e.all || len(e.wants) == 0 {
		return
	}
	e.buf = append(e.buf, b...)
	if len(e.buf) > 1<<20 {
		e.buf = append([]byte(nil), e.buf[len(e.buf)-(512<<10):]...)
	}
	for i, w := range e.wants {
		if !e.hit[i] && bytes.Contains(e.buf, []byte(w)) {
			e.hit[i] = true
		}
	}
	for _, h := range e.hit {
		if !h {
			return
		}
	}
	e.all = true
}

func (e *expectSet) missing() []string {
	var out []string
	for i, w := range e.wants {
		if !e.hit[i] {
			out = append(out, w)
		}
	}
	return out
}

func main() {
	var (
		path       = flag.String("fw", "", "firmware file (.uf2, .elf or .bin)")
		steps      = flag.Uint64("steps", 50_000_000, "maximum cycles to execute")
		secs       = flag.Float64("seconds", 0, "virtual seconds to run (overrides -steps)")
		unhandled  = flag.Bool("unhandled", false, "report unmapped peripheral accesses at exit")
		tracePins  = flag.String("trace-pins", "", "comma separated GPIO numbers to log")
		stopOnMMIO = flag.Bool("strict-mmio", false, "halt on the first unmapped peripheral access")
		core1      = flag.Bool("core1", true, "start core 1")
		quiet      = flag.Bool("quiet", false, "suppress the firmware header and final summary")
		traceN     = flag.Int("trace", 0, "trace the first N instructions")
		traceLast  = flag.Bool("trace-last", false, "dump the last 64 instructions on a fault")
		bpStr      = flag.String("bp", "", "break at this address (hex)")
		watchStr   = flag.String("watch", "", "log writes to this address (hex)")
		bp2Str     = flag.String("bp2", "", "secondary trace address (hex)")
		pokeStr    = flag.String("poke", "", "comma separated addr=val pokes (hex) after boot")
		sampleN    = flag.Int("sample", 0, "run in 10M cycle chunks and print the PC each time")
		msgFlag    = flag.Bool("msg", false, "at a breakpoint, decode r1 as a runtime message string")
		valStr     = flag.String("watch-val", "", "log writes of this value (hex)")
		lcdFlag    = flag.Bool("lcd", false, "attach an ST7789 display on SPI1 (RP2350-GEEK wiring)")
		lcdPNG     = flag.String("lcd-png", "", "write the LCD framebuffer to this PNG at exit")
		ethFlag    = flag.Bool("eth", false, "attach a W5500 Ethernet controller on SPI0 (RP2350-POE-ETH wiring)")
		w6300Flag  = flag.Bool("w6300", false, "attach a W6300 Ethernet controller on SPI0 (RP2350-POE-ETH wiring)")
		ch9120Flag = flag.Bool("ch9120", false, "attach a CH9120 UART-Ethernet module (RP2350-ETH wiring)")
		wsPin      = flag.Int("ws2812", -1, "decode a WS2812B strip driven on this GPIO")
		inputStr   = flag.String("input", "", "send these bytes to USB CDC and UART0 after boot")
		inputFile  = flag.String("input-file", "", "send the contents of this file at -input-at")
		inputAt    = flag.Duration("input-at", 200*time.Millisecond, "delay before -input/-input-file are sent")
		stdinFlag  = flag.Bool("stdin", false, "forward host stdin to USB CDC and UART0")
		stopExpect = flag.Bool("stop-on-expect", false, "stop as soon as all -expect strings are seen")
		wallLimit  = flag.Duration("wall-timeout", 0, "fail after this much wall time (0 = unlimited)")
		summaryOut = flag.String("summary-file", "", "write a JSON run summary to this path")
		allowHalt  = flag.Bool("allow-halt", false, "exit 0 even when a core halts")
		serialTCP  = flag.String("serial-tcp", "", "bridge UARTs to TCP: uart:addr[,uart:addr] (use 127.0.0.1:0 for an automatic port)")
		cpuProfile = flag.String("cpuprofile", "", "write a CPU profile to this path")
		peekStr    = flag.String("peek", "", "print memory at this address (hex) at exit")
		fromStr    = flag.String("trace-from", "", "start tracing when this address (hex) is reached")
		regStr     = flag.String("watch-reg", "", "log every change of this register")
	)
	var expectFlag stringList
	flag.Var(&expectFlag, "expect", "fail unless this console output appears (repeatable)")
	flag.Parse()
	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}
	if *path == "" {
		flag.Usage()
		os.Exit(2)
	}

	img, err := firmware.Load(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		os.Exit(1)
	}
	if !*quiet {
		fmt.Fprintf(os.Stderr, "firmware: %s, %d segment(s), %d bytes, entry=%#08x\n", img.Format, len(img.Segments), img.Size(), img.Entry)
	}

	c := rp2350.New()
	for _, seg := range img.Segments {
		c.LoadSegment(seg.Addr, seg.Data)
	}
	c.Boot()
	if !*core1 {
		c.CPU1.Parked = true
	}

	var lcd *devices.ST7789
	if *lcdFlag || *lcdPNG != "" {
		lcd = devices.NewST7789(c, 9, 8, 12)
		lcd.Debug = os.Getenv("RP2350SIM_LCD_LOG") != ""
	}

	if os.Getenv("RP2350SIM_DMA_LOG") != "" {
		c.OnDMA = func(ch int, ra, wa, count, ctrl uint32) {
			fmt.Fprintf(os.Stderr, "dma ch=%d read=%#08x write=%#08x count=%d ctrl=%#08x\n", ch, ra, wa, count, ctrl)
		}
	}
	if *ethFlag && *w6300Flag {
		fmt.Fprintln(os.Stderr, "-eth and -w6300 are mutually exclusive")
		os.Exit(2)
	}

	var w5500 *devices.W5500
	var w6300 *devices.W6300
	if *w6300Flag {
		w6300 = devices.NewW6300(c, 17)
		c.CycleHooks = append(c.CycleHooks, w6300.Tick)
		if os.Getenv("RP2350SIM_W6300_LOG") != "" {
			w6300.Logf = func(format string, args ...any) {
				fmt.Fprintf(os.Stderr, format+"\n", args...)
			}
		}
	}
	if *ethFlag {
		w5500 = devices.NewW5500(c, 17)
		c.CycleHooks = append(c.CycleHooks, w5500.Tick)
		if os.Getenv("RP2350SIM_ETH_LOG") != "" {
			w5500.Logf = func(format string, args ...any) {
				fmt.Fprintf(os.Stderr, format+"\n", args...)
			}
		}
	}

	if *wsPin >= 0 {
		ws := devices.NewWS2812B(c, *wsPin)
		ws.Logf = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "ws2812: "+format+"\n", args...)
		}
		c.CycleHooks = append(c.CycleHooks, ws.Tick)
	}

	spiLog := os.Getenv("RP2350SIM_SPI_LOG") != ""
	c.OnSPI0 = func(tx byte) byte {
		if w6300 != nil {
			return w6300.Transfer(tx)
		}
		if w5500 != nil {
			return w5500.Transfer(tx)
		}
		if spiLog {
			fmt.Fprintf(os.Stderr, "spi0 tx %02x\n", tx)
		}
		return 0xFF
	}
	c.OnSPI1 = func(tx byte) byte {
		if lcd != nil {
			return lcd.Transfer(tx)
		}
		if spiLog {
			fmt.Fprintf(os.Stderr, "spi1 tx %02x\n", tx)
		}
		return 0xFF
	}
	expects := &expectSet{}
	for _, w := range expectFlag {
		expects.add(w)
	}
	c.OnUART0 = func(b byte) { expects.console([]byte{b}) }
	c.OnUART1 = func(b byte) { expects.console([]byte{b}) }
	c.SetUSBCDC(expects.console)

	if *ch9120Flag {
		ch := devices.NewCH9120(c, 1, 18, 19, 17)
		if os.Getenv("RP2350SIM_CH9120_LOG") != "" {
			ch.Logf = func(format string, args ...any) {
				fmt.Fprintf(os.Stderr, format+"\n", args...)
			}
		}
		ch.Attach()
	}

	var bridges []*devices.UARTBridge
	if *serialTCP != "" {
		for _, spec := range strings.Split(*serialTCP, ",") {
			uartStr, addr, ok := strings.Cut(spec, ":")
			if !ok || addr == "" {
				fmt.Fprintf(os.Stderr, "serial-tcp: bad spec %q (want uart:host:port)\n", spec)
				continue
			}
			idx, err := strconv.Atoi(uartStr)
			if err != nil || idx < 0 || idx > 1 {
				fmt.Fprintf(os.Stderr, "serial-tcp: bad UART %q\n", uartStr)
				continue
			}
			br := devices.NewUARTBridge(c, idx, addr)
			br.Logf = func(format string, args ...any) {
				fmt.Fprintf(os.Stderr, format+"\n", args...)
			}
			if err := br.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "serial-tcp: uart%d: %v\n", idx, err)
				continue
			}
			bridges = append(bridges, br)
			if idx == 0 {
				prev := c.OnUART0
				c.OnUART0 = func(b byte) {
					if prev != nil {
						prev(b)
					}
					br.Write([]byte{b})
				}
			} else {
				prev := c.OnUART1
				c.OnUART1 = func(b byte) {
					if prev != nil {
						prev(b)
					}
					br.Write([]byte{b})
				}
			}
		}
		defer func() {
			for _, br := range bridges {
				br.Close()
			}
		}()
	}
	if os.Getenv("RP2350SIM_USB_LOG") != "" {
		c.USBLog = os.Stderr
	}

	if *tracePins != "" {
		wanted := map[int]bool{}
		for _, s := range strings.Split(*tracePins, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err == nil {
				wanted[n] = true
			}
		}
		c.Pins.OnChange = func(pin int, level bool) {
			if wanted[pin] {
				fmt.Fprintf(os.Stderr, "%10.3fms gp%d=%v\n", float64(c.NowMicros())/1000, pin, level)
			}
		}
	}

	if *traceN > 0 {
		n := 0
		var from uint64
		if *fromStr != "" {
			from, _ = strconv.ParseUint(strings.TrimPrefix(*fromStr, "0x"), 16, 32)
		}
		started := from == 0
		c.CPU0.TracePC = func(cpu *m33.CPU, pc uint32, hw1 uint16) {
			if !started {
				if uint64(pc) != from {
					return
				}
				started = true
			}
			if n >= *traceN {
				return
			}
			n++
			fmt.Fprintf(os.Stderr, "%08x %04x r0=%08x r1=%08x r2=%08x r3=%08x r4=%08x r7=%08x r10=%08x sp=%08x lr=%08x\n",
				pc, hw1, cpu.R[0], cpu.R[1], cpu.R[2], cpu.R[3], cpu.R[4], cpu.R[7], cpu.R[10], cpu.R[13], cpu.R[14])
		}
	}

	var bp uint64
	if *bpStr != "" {
		bp, _ = strconv.ParseUint(strings.TrimPrefix(*bpStr, "0x"), 16, 32)
	}
	var bp2 uint64
	if *bp2Str != "" {
		bp2, _ = strconv.ParseUint(strings.TrimPrefix(*bp2Str, "0x"), 16, 32)
	}
	var ring []string
	zeros := 0
	bpHits := 0
	if *traceLast {
		watchReg := -1
		lastReg := uint32(0)
		if *regStr != "" {
			n, _ := strconv.Atoi(*regStr)
			watchReg = n
		}
		maxAddr := img.MaxAddr()
		c.CPU0.TracePC = func(cpu *m33.CPU, pc uint32, hw1 uint16) {
			if pc >= maxAddr && pc < 0x10400000 {
				fmt.Fprintf(os.Stderr, "OUTSIDE IMAGE pc=%#08x lr=%#08x sp=%#08x\n", pc, cpu.R[14], cpu.R[13])
				start := 0
				if len(ring) > 40 {
					start = len(ring) - 40
				}
				for _, l := range ring[start:] {
					fmt.Fprintln(os.Stderr, "  ", l)
				}
				c.Halted = true
				os.Exit(1)
			}
			if watchReg >= 0 {
				if v := cpu.R[watchReg]; v != lastReg {
					fmt.Fprintf(os.Stderr, "r%d: %#08x -> %#08x at pc=%#08x (hw=%04x)\n", watchReg, lastReg, v, pc, hw1)
					lastReg = v
				}
			}
			if bp2 != 0 && uint64(pc) == bp2 {
				fmt.Fprintf(os.Stderr, "hit2 %#08x r0=%08x r1=%08x r2=%08x r3=%08x r4=%08x r5=%08x r6=%08x r7=%08x r8=%08x r9=%08x r10=%08x r11=%08x sp=%08x lr=%08x\n", pc, cpu.R[0], cpu.R[1], cpu.R[2], cpu.R[3], cpu.R[4], cpu.R[5], cpu.R[6], cpu.R[7], cpu.R[8], cpu.R[9], cpu.R[10], cpu.R[11], cpu.R[13], cpu.R[14])
			}
			if bp != 0 && uint64(pc) == bp {
				if *msgFlag {
					p, n := c.Read32(cpu.R[1]), c.Read32(cpu.R[1]+4)
					b := make([]byte, 0, n)
					for i := uint32(0); i < n && i < 200; i++ {
						b = append(b, c.Read8(p+i))
					}
					fmt.Fprintf(os.Stderr, "message: %q\n", string(b))
				}
				fmt.Fprintf(os.Stderr, "break at %#08x r0=%08x r1=%08x r2=%08x r3=%08x r4=%08x r5=%08x r6=%08x r7=%08x r8=%08x r9=%08x r10=%08x r11=%08x sp=%08x lr=%08x\n", pc, cpu.R[0], cpu.R[1], cpu.R[2], cpu.R[3], cpu.R[4], cpu.R[5], cpu.R[6], cpu.R[7], cpu.R[8], cpu.R[9], cpu.R[10], cpu.R[11], cpu.R[13], cpu.R[14])
				bpHits++
				if bpHits >= 200 {
					c.Halted = true
					os.Exit(1)
				} else {
					return
				}
			}
			if hw1 == 0 {
				zeros++
			} else {
				zeros = 0
				line := fmt.Sprintf("%08x %04x r0=%08x r1=%08x r2=%08x r3=%08x sp=%08x lr=%08x", pc, hw1, cpu.R[0], cpu.R[1], cpu.R[2], cpu.R[3], cpu.R[13], cpu.R[14])
				if len(ring) < 4096 {
					ring = append(ring, line)
				} else {
					copy(ring, ring[1:])
					ring[4095] = line
				}
			}
			if zeros > 64 {
				start := 0
				if len(ring) > 60 {
					start = len(ring) - 60
				}
				for _, l := range ring[start:] {
					fmt.Fprintln(os.Stderr, "  ", l)
				}
				c.Halted = true
				os.Exit(1)
			}
		}
	}

	for _, kv := range strings.Split(*pokeStr, ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		a, _ := strconv.ParseUint(strings.TrimPrefix(parts[0], "0x"), 16, 32)
		v, _ := strconv.ParseUint(strings.TrimPrefix(parts[1], "0x"), 16, 32)
		c.Write32(uint32(a), uint32(v))
	}

	if *valStr != "" {
		wv, _ := strconv.ParseUint(strings.TrimPrefix(*valStr, "0x"), 16, 32)
		c.OnWrite = func(addr uint32, val uint32, size int) {
			if val == uint32(wv) {
				fmt.Fprintf(os.Stderr, "write %#08x of %#08x size %d pc=%#08x lr=%#08x\n", addr, val, size, c.CPU0.R[15], c.CPU0.R[14])
			}
		}
	}

	if *watchStr != "" {
		wa, _ := strconv.ParseUint(strings.TrimPrefix(*watchStr, "0x"), 16, 32)
		c.OnWrite = func(addr uint32, val uint32, size int) {
			if uint64(addr) == wa {
				fmt.Fprintf(os.Stderr, "write %#08x = %#08x (size %d) pc=%#08x lr=%#08x\n", addr, val, size, c.CPU0.R[15], c.CPU0.R[14])
			}
		}
	}

	if *stopOnMMIO {
		c.OnUnhandled = func(addr uint32, write bool, val uint32) {
			op := "read"
			if write {
				op = "write"
			}
			c.Halted = true
			fmt.Fprintf(os.Stderr, "unmapped %s at %#08x val=%#08x pc=%#08x\n", op, addr, val, c.CPU0.R[15])
			for _, l := range ring {
				fmt.Fprintln(os.Stderr, "  ", l)
			}
		}
	}

	// Console input: USB CDC is flow-controlled by the emulated host, UART0 is
	// queue-safe, so stdin and -input feed both without disturbing the CPU.
	var usbPending []byte
	flushUSB := func() {
		for len(usbPending) > 0 && c.USBCDCReady() {
			n := len(usbPending)
			if n > 64 {
				n = 64
			}
			c.SendUSBCDC(usbPending[:n])
			usbPending = usbPending[n:]
		}
	}
	queueConsole := func(data []byte) {
		c.SendUART(0, data)
		usbPending = append(usbPending, data...)
		if len(usbPending) > 8192 {
			usbPending = append([]byte(nil), usbPending[len(usbPending)-4096:]...)
		}
		flushUSB()
	}

	var stdinMu sync.Mutex
	var stdinBuf []byte
	if *stdinFlag {
		go func() {
			buf := make([]byte, 256)
			for {
				stdinMu.Lock()
				full := len(stdinBuf) > 64*1024
				stdinMu.Unlock()
				if full {
					time.Sleep(time.Millisecond)
					continue
				}
				n, err := os.Stdin.Read(buf)
				if n > 0 {
					stdinMu.Lock()
					stdinBuf = append(stdinBuf, buf[:n]...)
					stdinMu.Unlock()
				}
				if err != nil {
					return
				}
			}
		}()
	}
	drainStdin := func() {
		stdinMu.Lock()
		n := len(stdinBuf)
		if n > 64 {
			n = 64
		}
		var chunk []byte
		if n > 0 {
			chunk = append([]byte(nil), stdinBuf[:n]...)
			stdinBuf = stdinBuf[n:]
		}
		stdinMu.Unlock()
		if len(chunk) > 0 {
			queueConsole(chunk)
		}
	}

	inputData := []byte(*inputStr)
	if *inputFile != "" {
		b, err := os.ReadFile(*inputFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "input-file:", err)
			os.Exit(1)
		}
		inputData = append(inputData, b...)
	}
	inputCycles := uint64(0)
	if *inputAt > 0 {
		inputCycles = uint64(float64(*inputAt) / float64(time.Second) * 150_000_000)
	}

	start := time.Now()
	timedOut := false
	if *sampleN == 0 {
		runCycles := uint64(*steps)
		if *secs > 0 {
			runCycles = uint64(*secs * 150_000_000)
		}
		if len(inputData) > 0 {
			warm := inputCycles
			if warm > runCycles {
				warm = runCycles
			}
			if warm > 0 {
				c.Run(warm)
				runCycles -= warm
				flushUSB()
				if *stdinFlag {
					drainStdin()
				}
			}
			queueConsole(inputData)
		}
		const chunk = 1_000_000
		for runCycles > 0 {
			if c.CPU0.Halted || c.CPU1.Halted || c.Halted {
				break
			}
			if *stopExpect && expects.all {
				break
			}
			if *wallLimit > 0 && time.Since(start) > *wallLimit {
				timedOut = true
				break
			}
			n := runCycles
			if n > chunk {
				n = chunk
			}
			c.Run(n)
			runCycles -= n
			flushUSB()
			if *stdinFlag {
				drainStdin()
			}
		}
		flushUSB()
	} else {
		for i := 0; i < *sampleN; i++ {
			c.Run(10_000_000)
			fmt.Fprintf(os.Stderr, "sample %d: pc=%#08x lr=%#08x sp=%#08x r0=%#08x r1=%#08x\n", i, c.CPU0.R[15], c.CPU0.R[14], c.CPU0.R[13], c.CPU0.R[0], c.CPU0.R[1])
			if c.CPU0.Halted || c.Halted {
				break
			}
		}
	}
	wall := time.Since(start)
	halted := c.CPU0.Halted || c.CPU1.Halted || c.Halted
	missing := expects.missing()

	exitCode := 0
	switch {
	case timedOut:
		exitCode = 5
	case len(missing) > 0:
		exitCode = 4
	case halted && !*allowHalt:
		exitCode = 3
	}
	if timedOut {
		fmt.Fprintf(os.Stderr, "wall timeout after %s\n", *wallLimit)
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "missing expected output: %q\n", missing)
	}

	if !*quiet {
		fmt.Fprintf(os.Stderr, "cycles=%d (%.3fs virtual) wall=%s\n", c.Cycles, float64(c.Cycles)/150e6, wall)
		fmt.Fprintf(os.Stderr, "core0 pc=%#08x sp=%#08x lr=%#08x\n", c.CPU0.R[15], c.CPU0.R[13], c.CPU0.R[14])
		fmt.Fprintf(os.Stderr, "core1 pc=%#08x sp=%#08x lr=%#08x parked=%v\n", c.CPU1.R[15], c.CPU1.R[13], c.CPU1.R[14], c.CPU1.Parked)
		fmt.Fprintf(os.Stderr, "irq delivered: %d\n", c.IRQCount)
		fmt.Fprintf(os.Stderr, "sio pushes=%d core1 launches=%d\n", c.SIOPushes(), c.Core1Launches())
		fmt.Fprintf(os.Stderr, "runqueue head=%#08x tail=%#08x sleepQueue=%#08x currentTask=%#08x mainExited=%d\n",
			c.Read32(0x200017c0), c.Read32(0x200017c4), c.Read32(0x2000179c), c.Read32(0x2000175c), c.Read32(0x20001758))
	}
	if c.CPU0.Halted {
		fmt.Fprintf(os.Stderr, "core0 halted: %s pc=%#08x lr=%#08x sp=%#08x\n", c.CPU0.HaltReason, c.CPU0.R[15], c.CPU0.R[14], c.CPU0.R[13])
	}
	if c.CPU1.Halted {
		fmt.Fprintf(os.Stderr, "core1 halted: %s pc=%#08x lr=%#08x sp=%#08x\n", c.CPU1.HaltReason, c.CPU1.R[15], c.CPU1.R[14], c.CPU1.R[13])
	}
	if lcd != nil && !*quiet {
		fmt.Fprintf(os.Stderr, "lcd: pixels=%d on=%v\n", lcd.PixelsWritten(), lcd.On())
	}
	if lcd != nil && *lcdPNG != "" {
		if err := lcd.WritePNG(*lcdPNG); err != nil {
			fmt.Fprintf(os.Stderr, "lcd png: %v\n", err)
		}
	}

	if *peekStr != "" {
		a, _ := strconv.ParseUint(strings.TrimPrefix(*peekStr, "0x"), 16, 32)
		fmt.Fprintf(os.Stderr, "mem[%#08x] = %#08x\n", uint32(a), c.Read32(uint32(a)))
	}

	if *unhandled {
		fmt.Fprintln(os.Stderr, "unmapped accesses:")
		fmt.Fprint(os.Stderr, c.DescribeUnhandled())
	}

	if *summaryOut != "" {
		sum := struct {
			Firmware       string   `json:"firmware"`
			Format         string   `json:"format"`
			Cycles         uint64   `json:"cycles"`
			VirtualSeconds float64  `json:"virtual_seconds"`
			WallSeconds    float64  `json:"wall_seconds"`
			Halted         bool     `json:"halted"`
			HaltReason     string   `json:"halt_reason,omitempty"`
			Core0PC        uint32   `json:"core0_pc"`
			Core0SP        uint32   `json:"core0_sp"`
			Core1PC        uint32   `json:"core1_pc"`
			Core1SP        uint32   `json:"core1_sp"`
			Core1Parked    bool     `json:"core1_parked"`
			IRQs           uint64   `json:"irqs"`
			Core1Launches  uint64   `json:"core1_launches"`
			ExpectMatched  bool     `json:"expectations_matched"`
			ExpectMissing  []string `json:"missing_expectations,omitempty"`
			TimedOut       bool     `json:"wall_timed_out"`
			ExitCode       int      `json:"exit_code"`
		}{
			Firmware:       *path,
			Format:         img.Format,
			Cycles:         c.Cycles,
			VirtualSeconds: float64(c.Cycles) / 150e6,
			WallSeconds:    wall.Seconds(),
			Halted:         halted,
			Core0PC:        c.CPU0.R[15],
			Core0SP:        c.CPU0.R[13],
			Core1PC:        c.CPU1.R[15],
			Core1SP:        c.CPU1.R[13],
			Core1Parked:    c.CPU1.Parked,
			IRQs:           c.IRQCount,
			Core1Launches:  c.Core1Launches(),
			ExpectMatched:  len(missing) == 0 && len(expects.wants) > 0,
			ExpectMissing:  missing,
			TimedOut:       timedOut,
			ExitCode:       exitCode,
		}
		if c.CPU0.Halted {
			sum.HaltReason = c.CPU0.HaltReason
		} else if c.CPU1.Halted {
			sum.HaltReason = c.CPU1.HaltReason
		}
		b, _ := json.MarshalIndent(sum, "", "  ")
		b = append(b, '\n')
		if err := os.WriteFile(*summaryOut, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "summary-file:", err)
			if exitCode == 0 {
				exitCode = 1
			}
		}
	}

	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
