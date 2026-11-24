// Command rp2350sim runs RP2350 firmware headlessly.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/akhenakh/rp2350sim/internal/debug"
	"github.com/akhenakh/rp2350sim/internal/devices"
	"github.com/akhenakh/rp2350sim/internal/firmware"
	"github.com/akhenakh/rp2350sim/internal/gdb"
	"github.com/akhenakh/rp2350sim/internal/logging"
	"github.com/akhenakh/rp2350sim/internal/m33"
	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// version is set by GoReleaser with -X main.version.
var version = "dev"

// stopCPUProfile is installed when -cpuprofile starts. os.Exit skips defers,
// and the failing runs are exactly the ones worth profiling, so exit() flushes
// the profile explicitly.
var stopCPUProfile func()

func exit(code int) {
	if stopCPUProfile != nil {
		stopCPUProfile()
		stopCPUProfile = nil
	}
	os.Exit(code)
}

// hex renders an address or register value for a log attribute.
func hex(v uint32) string { return fmt.Sprintf("%#08x", v) }

// resolveAddress accepts a hex address or an ELF symbol name.
func resolveAddress(spec string, info *debug.Info) (uint32, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, nil
	}
	if info != nil {
		if addr, ok := info.Resolve(spec); ok {
			return addr, nil
		}
	}
	raw := strings.TrimPrefix(strings.TrimPrefix(spec, "0x"), "0X")
	if raw != "" {
		if v, err := strconv.ParseUint(raw, 16, 32); err == nil {
			return uint32(v), nil
		}
	}
	return 0, fmt.Errorf("%q is neither a hex address nor a known symbol", spec)
}

func newRunnerCard(c *rp2350.Chip, cs int, image, dir string) (*devices.SDCard, error) {
	return devices.NewSDCard(c, cs, image, dir)
}

// regAttrs describes a core's register set for break and trace records.
func regAttrs(cpu *m33.CPU) []any {
	attrs := make([]any, 0, 16)
	for i := range 13 {
		attrs = append(attrs, slog.String(fmt.Sprintf("r%d", i), hex(cpu.R[i])))
	}
	return append(attrs, slog.String("sp", hex(cpu.R[13])), slog.String("lr", hex(cpu.R[14])))
}

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
	// A broken stdout (closed pipe) must not stop the run: the firmware keeps
	// executing and the exit code still reports the emulator's own verdict.
	_, _ = os.Stdout.Write(b)
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
		path        = flag.String("fw", "", "firmware file (.uf2, .elf or .bin)")
		steps       = flag.Uint64("steps", 50_000_000, "maximum cycles to execute")
		secs        = flag.Float64("seconds", 0, "virtual seconds to run (overrides -steps)")
		unhandled   = flag.Bool("unhandled", false, "report unmapped peripheral accesses at exit")
		tracePins   = flag.String("trace-pins", "", "comma separated GPIO numbers to log")
		logicPins   = flag.String("logic-pins", "", "comma separated GPIOs to capture for the logic analyzer")
		logicOut    = flag.String("logic-out", "", "write the logic analyzer trace here (default: auto name)")
		vcdOut      = flag.String("vcd", "", "write the logic capture as an IEEE 1364 VCD")
		dviPNG      = flag.String("dvi-png", "", "write the last HSTX/DVI frame to this PNG")
		logicUART   = flag.String("logic-uart", "", "decode a UART pin trace: tx[,rx],baud")
		logicI2C    = flag.String("logic-i2c", "", "decode an I2C pin trace: sda,scl")
		logicSPI    = flag.String("logic-spi", "", "decode an SPI pin trace: sck,mosi[,miso[,cs]]")
		logicRate   = flag.Float64("logic-rate", 2e6, "logic analyzer sample rate in Hz")
		logicDepth  = flag.Int("logic-depth", devices.LogicDepth, "logic analyzer ring depth in samples")
		logicBoard  = flag.String("logic-board", "", "board label for the logic trace and filename")
		busTimeout  = flag.Duration("bus-timeout", time.Second, "I2C/SPI TCP bridge request timeout")
		busWait     = flag.Duration("bus-wait", 2*time.Second, "I2C/SPI TCP bridge first-client wait")
		stopOnMMIO  = flag.Bool("strict-mmio", false, "halt on the first unmapped peripheral access")
		core1       = flag.Bool("core1", true, "start core 1")
		quiet       = flag.Bool("quiet", false, "suppress the firmware header and final summary")
		traceN      = flag.Int("trace", 0, "trace the first N instructions")
		traceLast   = flag.Bool("trace-last", false, "dump the last 64 instructions on a fault")
		bpStr       = flag.String("bp", "", "log a breakpoint at this address or symbol")
		watchStr    = flag.String("watch", "", "log writes to this address or symbol")
		bp2Str      = flag.String("bp2", "", "secondary breakpoint address or symbol")
		pokeStr     = flag.String("poke", "", "comma separated addr=val pokes (hex) after boot")
		sampleN     = flag.Int("sample", 0, "run in 10M cycle chunks and print the PC each time")
		msgFlag     = flag.Bool("msg", false, "at a breakpoint, decode r1 as a runtime message string")
		valStr      = flag.String("watch-val", "", "log writes of this value (hex)")
		lcdFlag     = flag.Bool("lcd", false, "attach an ST7789 display on SPI1 (RP2350-GEEK wiring)")
		lcdPNG      = flag.String("lcd-png", "", "write the LCD framebuffer to this PNG at exit")
		touchLCD2   = flag.Bool("touch-lcd2", false, "attach the RP2350-Touch-LCD-2 hardware (touch LCD, IMU, battery, microSD)")
		touchStr    = flag.String("touch", "", "inject a touch at x,y in native panel coordinates")
		touchAt     = flag.Duration("touch-at", 150*time.Millisecond, "delay before -touch is applied")
		touchHold   = flag.Duration("touch-hold", 100*time.Millisecond, "how long -touch stays down")
		sensorsOut  = flag.Bool("sensors", false, "print touch, IMU and battery state at exit")
		sdFlag      = flag.Bool("sd", false, "attach a microSD card on SPI0 CS GP23 (RP2350-GEEK wiring)")
		sdImage     = flag.String("sd-image", "", "backing image for -sd or -touch-lcd2 (default: built-in FAT16)")
		sdDir       = flag.String("sd-dir", "", "back the card with a host directory (read/write files from the host)")
		otpIn       = flag.String("otp", "", "load OTP rows from this JSON file before boot")
		otpOut      = flag.String("otp-dump", "", "write programmed OTP rows to this JSON file at exit")
		flashIn     = flag.String("flash", "", "load a raw flash image and write it back (enables self-update)")
		flashOut    = flag.String("flash-out", "", "write the final raw flash image to this path")
		psramMB     = flag.Int("psram", 0, "attach PSRAM on QMI CS1 with this size in MiB (0 disables)")
		psramLat    = flag.Int("psram-latency", 4, "extra cycles charged for each PSRAM access")
		psramCS     = flag.Int("psram-cs", 8, "QMI CS1 GPIO used by the PSRAM device")
		rp2350b     = flag.Bool("rp2350b", false, "model the RP2350B QFN-80 package (48 GPIOs)")
		ethFlag     = flag.Bool("eth", false, "attach a W5500 Ethernet controller on SPI0 (breadboard wiring, CS GP17)")
		w6300Flag   = flag.Bool("w6300", false, "attach a W6300 Ethernet controller on SPI0 (RP2350-POE-ETH wiring)")
		ch9120Flag  = flag.Bool("ch9120", false, "attach a CH9120 UART-Ethernet module (RP2350-ETH wiring)")
		wsPin       = flag.Int("ws2812", -1, "decode a WS2812B strip driven on this GPIO")
		inputStr    = flag.String("input", "", "send these bytes to USB CDC and UART0 after boot")
		inputFile   = flag.String("input-file", "", "send the contents of this file at -input-at")
		inputAt     = flag.Duration("input-at", 200*time.Millisecond, "delay before -input/-input-file are sent")
		stdinFlag   = flag.Bool("stdin", false, "forward host stdin to USB CDC and UART0")
		stopExpect  = flag.Bool("stop-on-expect", false, "stop as soon as all -expect strings are seen")
		wallLimit   = flag.Duration("wall-timeout", 0, "fail after this much wall time (0 = unlimited)")
		summaryOut  = flag.String("summary-file", "", "write a JSON run summary to this path")
		allowHalt   = flag.Bool("allow-halt", false, "exit 0 even when a core halts")
		keepAfter   = flag.Bool("continue-after-halt", false, "keep peripherals running after a core halts (PIO/PWM keep driving)")
		serialTCP   = flag.String("serial-tcp", "", "bridge UARTs to TCP: uart:addr[,uart:addr] (use 127.0.0.1:0 for an automatic port)")
		cpuProfile  = flag.String("cpuprofile", "", "write a CPU profile to this path")
		peekStr     = flag.String("peek", "", "print memory at this address (hex) at exit")
		fromStr     = flag.String("trace-from", "", "start tracing when this address (hex) is reached")
		regStr      = flag.String("watch-reg", "", "log every change of this register")
		gdbAddr     = flag.String("gdb", "", "serve the GDB remote protocol on this address (e.g. 127.0.0.1:3333)")
		audioOut    = flag.String("audio-out", "", "write decoded audio to this WAV file")
		audioI2S    = flag.String("audio-i2s", "", "decode I2S audio on bclk,lrclk,data[,bits]")
		audioPDM    = flag.String("audio-pdm", "", "decode PDM audio on data,clock[,decimation]")
		audioPWM    = flag.Int("audio-pwm", -1, "decode PWM/GPIO audio on this GPIO")
		audioRate   = flag.Int("audio-rate", 48000, "audio sample rate in Hz")
		usbMIDI     = flag.Bool("usb-midi", false, "enable the USB-MIDI class endpoint")
		midiUART    = flag.Int("midi-uart", -1, "tap this UART (0/1) as 31250-baud MIDI")
		midiMonitor = flag.Bool("midi-monitor", false, "log parsed MIDI messages")
		midiUDP     = flag.String("midi-udp", "", "bidirectional UDP-MIDI endpoint (e.g. 127.0.0.1:5004)")
		logFormat   = flag.String("log-format", "auto", "diagnostic log format: auto, text or json")
		logLevel    = flag.String("log-level", "info", "diagnostic log level: debug, info, warn or error")
		showVer     = flag.Bool("version", false, "print the version and exit")
	)
	var expectFlag stringList
	var i2cTCPFlag, spiTCPFlag stringList
	var i2cPresetFlag, spiPresetFlag stringList
	flag.Var(&expectFlag, "expect", "fail unless this console output appears (repeatable)")
	flag.Var(&i2cTCPFlag, "i2c-tcp", "I2C target over TCP: bus:addr[,addr][@listen|=connect] (repeatable)")
	flag.Var(&spiTCPFlag, "spi-tcp", "SPI device over TCP: bus:cs[@listen|=connect] (repeatable)")
	flag.Var(&i2cPresetFlag, "i2c-preset", "built-in I2C device: bus:name[@addr] (repeatable)")
	flag.Var(&spiPresetFlag, "spi-preset", "built-in SPI device: bus:name@cs (repeatable)")
	listPresets := flag.Bool("list-presets", false, "list built-in I2C/SPI device presets and exit")
	flag.Parse()
	if *listPresets {
		printBusPresets()
		return
	}
	explicitBudget := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "steps" || f.Name == "seconds" {
			explicitBudget = true
		}
	})
	if *showVer {
		fmt.Println("rp2350sim", version)
		return
	}
	if *sdImage != "" && *sdDir != "" {
		fmt.Fprintln(os.Stderr, "-sd-image and -sd-dir are mutually exclusive")
		exit(2)
	}
	if *gdbAddr != "" && *sampleN != 0 {
		fmt.Fprintln(os.Stderr, "-gdb cannot be combined with -sample")
		exit(2)
	}
	if *midiUART < -1 || *midiUART > 1 {
		fmt.Fprintln(os.Stderr, "-midi-uart must be 0, 1 or -1")
		exit(2)
	}
	if *psramMB < 0 || *psramMB > 16 || *psramMB&(*psramMB-1) != 0 {
		fmt.Fprintln(os.Stderr, "-psram must be 0, 1, 2, 4, 8 or 16 MiB")
		exit(2)
	}
	if *psramLat < 0 {
		fmt.Fprintln(os.Stderr, "-psram-latency must not be negative")
		exit(2)
	}
	if *psramCS < 0 || *psramCS > 47 {
		fmt.Fprintln(os.Stderr, "-psram-cs must be a GPIO number")
		exit(2)
	}
	logicEnabled := *logicPins != "" || *logicOut != "" || *logicUART != "" || *logicI2C != "" || *logicSPI != ""
	writeTrace := logicEnabled
	logicEnabled = logicEnabled || *vcdOut != ""
	logicLabel := *logicBoard
	if logicLabel == "" {
		if *path != "" {
			logicLabel = strings.TrimSuffix(filepath.Base(*path), filepath.Ext(*path))
		} else {
			logicLabel = "flash"
		}
	}
	var logicCap *logicCapture
	if logicEnabled {
		var logicErr error
		logicCap, logicErr = newLogicCapture(*logicPins, *logicUART, *logicI2C, *logicSPI, *logicRate, *logicDepth, logicLabel)
		if logicErr != nil {
			fmt.Fprintln(os.Stderr, "logic capture:", logicErr)
			exit(2)
		}
		if *vcdOut != "" && len(logicCap.pins) == 0 {
			fmt.Fprintln(os.Stderr, "-vcd needs -logic-pins or a -logic-uart/-logic-i2c/-logic-spi pin set")
			exit(2)
		}
	}

	format, err := logging.ParseFormat(*logFormat)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		exit(2)
	}
	level, err := logging.ParseLevel(*logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		exit(2)
	}
	log := logging.New(os.Stderr, format, level)
	// The legacy RP2350SIM_*_LOG switches select one diagnostic stream. They
	// predate -log-level, so each enabled stream gets a debug-level logger
	// instead of raising the level for every component.
	debugLog := func() *slog.Logger {
		if level <= slog.LevelDebug {
			return log
		}
		return logging.New(os.Stderr, format, slog.LevelDebug)
	}

	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			log.Error("cpu profile", slog.String("path", *cpuProfile), slog.Any("err", err))
			exit(1)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Error("cpu profile", slog.Any("err", err))
			exit(1)
		}
		stopCPUProfile = pprof.StopCPUProfile
	}
	if *path == "" && *flashIn == "" {
		flag.Usage()
		exit(2)
	}

	var img *firmware.Image
	fwFormat := "flash"
	if *path != "" {
		img, err = firmware.Load(*path)
		if err != nil {
			log.Error("firmware load failed", slog.String("path", *path), slog.Any("err", err))
			exit(1)
		}
		if err := img.ValidateBoot(rp2350.FlashBase, rp2350.FlashSize, rp2350.SRAMBase, rp2350.SRAMSize); err != nil {
			log.Error("unsupported firmware layout", slog.String("path", *path), slog.Any("err", err))
			exit(1)
		}
		fwFormat = img.Format
		if !*quiet {
			log.Info("firmware loaded",
				slog.String("path", *path),
				slog.String("format", img.Format),
				slog.Int("segments", len(img.Segments)),
				slog.Int("bytes", img.Size()),
				slog.String("entry", hex(img.Entry)))
		}
	}

	var dbgInfo *debug.Info
	if img != nil && img.Format == "elf" {
		dbgInfo, _ = debug.Load(*path)
	}
	c := rp2350.New()
	c.Logger = log
	if *rp2350b {
		c.SetPackage(rp2350.PackageQFN80)
	}
	c.ConfigurePSRAM(uint32(*psramMB)<<20, uint32(*psramLat), *psramCS)
	if *psramMB > 0 && !*quiet {
		log.Info("psram attached",
			slog.Int("mib", *psramMB),
			slog.Int("cs", *psramCS),
			slog.Int("latency_cycles", *psramLat))
	}
	if *flashIn != "" {
		data, ferr := os.ReadFile(*flashIn)
		switch {
		case ferr == nil:
			if len(data) > len(c.Flash) {
				log.Error("flash image too large", slog.String("path", *flashIn), slog.Int("bytes", len(data)))
				exit(1)
			}
			c.LoadFlashImage(data)
			if !*quiet {
				log.Info("flash loaded", slog.String("path", *flashIn), slog.Int("bytes", len(data)))
			}
		case os.IsNotExist(ferr) && img != nil:
			if !*quiet {
				log.Info("flash image missing; starting erased", slog.String("path", *flashIn))
			}
		default:
			log.Error("flash load", slog.String("path", *flashIn), slog.Any("err", ferr))
			exit(1)
		}
	}
	otpSeeded := 0
	if *otpIn != "" {
		otpSeeded, err = loadOTP(c, *otpIn)
		if err != nil {
			log.Error("otp load", slog.String("path", *otpIn), slog.Any("err", err))
			exit(1)
		}
		if !*quiet {
			log.Info("otp seeded", slog.String("path", *otpIn), slog.Int("rows", otpSeeded))
		}
	}
	if os.Getenv("RP2350SIM_DCP_LOG") != "" {
		c.CPU0.DCPLog = func(format m33.DCPFormat, op m33.DCPOp, x, y, result float64) {
			log.Debug("dcp", "format", format, "op", op, "x", x, "y", y, "result", result)
		}
	}
	if os.Getenv("RP2350SIM_USB_LOG") != "" || os.Getenv("RP2350SIM_PIO_LOG") != "" {
		c.Logger = debugLog()
	}
	if img != nil {
		for _, seg := range img.Segments {
			if err := c.ValidateSegment(seg.Addr, seg.Data); err != nil {
				log.Error("firmware segment validation failed", slog.String("path", *path), slog.Any("err", err))
				exit(1)
			}
		}
		for _, seg := range img.Segments {
			if err := c.LoadSegment(seg.Addr, seg.Data); err != nil {
				log.Error("firmware segment load failed", slog.String("path", *path), slog.Any("err", err))
				exit(1)
			}
		}
	} else {
		// Booting straight from a flash image: validate the vector table the
		// same way a host-loaded firmware is validated, following a partition
		// table to the slot the bootrom would run.
		head, err := rp2350.BootVector(c.Flash)
		if err != nil {
			log.Error("unsupported flash image", slog.String("path", *flashIn), slog.Any("err", err))
			exit(1)
		}
		bootImg := &firmware.Image{Format: "flash", Segments: []firmware.Segment{{Addr: rp2350.FlashBase, Data: head}}}
		if err := bootImg.ValidateBoot(rp2350.FlashBase, rp2350.FlashSize, rp2350.SRAMBase, rp2350.SRAMSize); err != nil {
			log.Error("unsupported flash image", slog.String("path", *flashIn), slog.Any("err", err))
			exit(1)
		}
	}
	c.Boot()
	if !*core1 {
		c.CPU1.Parked = true
	}
	var debugMu sync.Mutex
	var gdbSrv *gdb.Server
	var gdbDone chan struct{}
	flashPath := *flashOut
	if flashPath == "" {
		flashPath = *flashIn
	}
	savedWrites := ^uint64(0)
	saveFlash := func() {
		if flashPath == "" {
			return
		}
		debugMu.Lock()
		writes := c.FlashWrites()
		if writes == savedWrites {
			debugMu.Unlock()
			return
		}
		data := c.FlashCopy()
		debugMu.Unlock()
		if err := os.WriteFile(flashPath, data, 0o644); err != nil {
			log.Error("flash save", slog.String("path", flashPath), slog.Any("err", err))
			return
		}
		debugMu.Lock()
		unchanged := c.FlashWrites() == writes
		debugMu.Unlock()
		if !unchanged {
			return // a newer flash snapshot still needs saving
		}
		savedWrites = writes
		if !*quiet {
			log.Info("flash saved",
				slog.String("path", flashPath),
				slog.Int("bytes", len(data)),
				slog.Uint64("writes", writes))
		}
	}
	lastBoot := c.BootCount()

	var lcd *devices.ST7789
	var lcdSPI = 1
	if *lcdFlag || (*lcdPNG != "" && !*touchLCD2) {
		lcd = devices.NewST7789(c, 9, 8, 12, 135, 240)
		lcdSPI = 1
		if os.Getenv("RP2350SIM_LCD_LOG") != "" {
			lcd.Logger = debugLog()
		}
	}

	var touch *devices.CST816D
	var imu *devices.QMI8658
	var card *devices.SDCard
	spiCardBus := 1
	if *touchLCD2 {
		if *lcdFlag {
			log.Error("-lcd and -touch-lcd2 are mutually exclusive")
			exit(2)
		}
		lcd = devices.NewST7789(c, 17, 16, 20, 240, 320)
		lcdSPI = 0
		if os.Getenv("RP2350SIM_LCD_LOG") != "" {
			lcd.Logger = debugLog()
		}
		touch = devices.NewCST816D(c, 0x15, 20, 29)
		c.AttachI2C(0, 0x15, touch)
		c.CycleHooks = append(c.CycleHooks, touch.Tick)
		imu = devices.NewQMI8658(c, 0x6b, 14, -1)
		c.AttachI2C(0, 0x6b, imu)
		c.CycleHooks = append(c.CycleHooks, imu.Tick)
		c.Pins.SetAnalog(28, 3.9/3) // battery divider
		card, err = newRunnerCard(c, 25, *sdImage, *sdDir)
		if err != nil {
			log.Error("sd card", slog.Any("err", err))
			exit(1)
		}
	} else if *sdFlag {
		card, err = newRunnerCard(c, 23, *sdImage, *sdDir)
		if err != nil {
			log.Error("sd card", slog.Any("err", err))
			exit(1)
		}
		spiCardBus = 0
	}

	if os.Getenv("RP2350SIM_DMA_LOG") != "" {
		dmaLog := debugLog()
		c.OnDMA = func(ch int, ra, wa, count, ctrl uint32) {
			dmaLog.Debug("dma transfer",
				slog.Int("channel", ch),
				slog.String("read", hex(ra)),
				slog.String("write", hex(wa)),
				slog.Int("count", int(count)),
				slog.String("ctrl", hex(ctrl)))
		}
	}
	if *ethFlag && *w6300Flag {
		log.Error("-eth and -w6300 are mutually exclusive")
		exit(2)
	}

	var w5500 *devices.W5500
	var w6300 *devices.W6300
	var ch9120 *devices.CH9120
	if *w6300Flag {
		w6300 = devices.NewW6300(c, 17)
		c.CycleHooks = append(c.CycleHooks, w6300.Tick)
		if os.Getenv("RP2350SIM_W6300_LOG") != "" {
			w6300.Logger = debugLog()
		}
	}
	if *ethFlag {
		w5500 = devices.NewW5500(c, 17)
		c.CycleHooks = append(c.CycleHooks, w5500.Tick)
		if os.Getenv("RP2350SIM_ETH_LOG") != "" {
			w5500.Logger = debugLog()
		}
	}

	if *wsPin >= 0 {
		ws := devices.NewWS2812B(c, *wsPin)
		ws.Logger = log
		c.CycleHooks = append(c.CycleHooks, ws.Tick)
	}

	spiLog := os.Getenv("RP2350SIM_SPI_LOG") != ""
	spiLogger := log
	if spiLog {
		spiLogger = debugLog()
	}
	type spiDev struct {
		cs int
		fn func(byte) byte
	}
	var spi0, spi1 []spiDev
	if w6300 != nil {
		spi0 = append(spi0, spiDev{17, w6300.Transfer})
	}
	if w5500 != nil {
		spi0 = append(spi0, spiDev{17, w5500.Transfer})
	}
	if lcd != nil {
		if lcdSPI == 1 {
			spi1 = append(spi1, spiDev{9, lcd.Transfer})
		} else {
			spi0 = append(spi0, spiDev{17, lcd.Transfer})
		}
	}
	if card != nil {
		if spiCardBus == 0 {
			spi0 = append(spi0, spiDev{23, card.Transfer})
		} else {
			spi1 = append(spi1, spiDev{25, card.Transfer})
		}
	}
	i2cSpecs, err := parseBusBridgeSpecs(devices.BusI2C, i2cTCPFlag)
	if err != nil {
		log.Error("i2c-tcp", slog.Any("err", err))
		exit(2)
	}
	spiSpecs, err := parseBusBridgeSpecs(devices.BusSPI, spiTCPFlag)
	if err != nil {
		log.Error("spi-tcp", slog.Any("err", err))
		exit(2)
	}
	i2cPresets, err := parseBusPresetSpecs(devices.BusI2C, i2cPresetFlag)
	if err != nil {
		log.Error("i2c-preset", slog.Any("err", err))
		exit(2)
	}
	spiPresets, err := parseBusPresetSpecs(devices.BusSPI, spiPresetFlag)
	if err != nil {
		log.Error("spi-preset", slog.Any("err", err))
		exit(2)
	}
	addSPI := func(bus, cs int, fn func(byte) byte) {
		if bus == 0 {
			spi0 = append(spi0, spiDev{cs, fn})
		} else {
			spi1 = append(spi1, spiDev{cs, fn})
		}
	}
	var busBridges []*devices.BusBridge
	var spiBridges []*devices.SPIBridge
	busBridges, spiBridges, err = attachBusBridges(c, log, *busTimeout, *busWait, i2cSpecs, spiSpecs, addSPI)
	if err != nil {
		log.Error("bus bridge", slog.Any("err", err))
		exit(2)
	}
	defer func() {
		for _, br := range busBridges {
			br.Close()
		}
	}()
	if err := attachBusPresets(c, log, append(i2cPresets, spiPresets...), addSPI); err != nil {
		log.Error("bus preset", slog.Any("err", err))
		exit(2)
	}
	// A controller may carry several devices; each needs its own CS pin, and
	// a pin cannot serve two controllers.
	type spiKey struct{ bus, cs int }
	seenCS := map[spiKey]bool{}
	pinBus := map[int]int{}
	for bus, devs := range [][]spiDev{spi0, spi1} {
		for _, d := range devs {
			if d.cs < 0 {
				continue
			}
			k := spiKey{bus: bus, cs: d.cs}
			if seenCS[k] {
				log.Error("two SPI devices share a chip select",
					slog.Int("spi", bus), slog.Int("cs", d.cs))
				exit(2)
			}
			seenCS[k] = true
			if prev, ok := pinBus[d.cs]; ok && prev != bus {
				log.Error("one GPIO is used as CS on two SPI controllers",
					slog.Int("gpio", d.cs), slog.Int("spi", prev), slog.Int("other", bus))
				exit(2)
			}
			pinBus[d.cs] = bus
		}
	}
	if card != nil {
		c.CycleHooks = append(c.CycleHooks, card.Tick)
	}
	spiHandler := func(devs []spiDev) func(byte) byte {
		return func(tx byte) byte {
			rx := byte(0xFF)
			for _, d := range devs {
				if d.cs >= 0 && c.Pins.Level(d.cs) {
					continue
				}
				rx = d.fn(tx)
				break
			}
			if spiLog {
				spiLogger.Debug("spi tx", slog.String("byte", fmt.Sprintf("%02x", tx)), slog.String("rx", fmt.Sprintf("%02x", rx)))
			}
			return rx
		}
	}
	c.OnSPI0 = spiHandler(spi0)
	c.OnSPI1 = spiHandler(spi1)
	expects := &expectSet{}
	for _, w := range expectFlag {
		expects.add(w)
	}
	if os.Getenv("RP2350SIM_I2C_LOG") != "" {
		i2cLog := debugLog()
		c.OnI2C = func(bus int, cycles uint64, kind string, addr uint8, data byte, ack bool) {
			i2cLog.Debug("i2c", slog.Int("bus", bus), slog.String("kind", kind),
				slog.String("addr", fmt.Sprintf("%02x", addr)), slog.String("data", fmt.Sprintf("%02x", data)), slog.Bool("ack", ack))
		}
	}
	c.OnUART0 = func(b byte) { expects.console([]byte{b}) }
	c.OnUART1 = func(b byte) { expects.console([]byte{b}) }
	c.SetUSBCDC(expects.console)

	if *touchStr != "" {
		if touch == nil {
			log.Error("-touch needs -touch-lcd2")
			exit(2)
		}
		parts := strings.Split(*touchStr, ",")
		if len(parts) != 2 {
			log.Error("-touch expects x,y")
			exit(2)
		}
		x, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		y, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err1 != nil || err2 != nil {
			log.Error("-touch expects integer x,y")
			exit(2)
		}
		at := uint64(touchAt.Seconds() * 150e6)
		end := at + uint64(touchHold.Seconds()*150e6)
		var applied, released bool
		c.CycleHooks = append(c.CycleHooks, func() {
			if !applied && c.Cycles >= at {
				log.Debug("inject touch", "x", x, "y", y, "cycles", c.Cycles)
				touch.SetTouch(x, y, true)
				applied = true
			}
			if applied && !released && c.Cycles >= end {
				log.Debug("release touch", "cycles", c.Cycles)
				touch.SetTouch(x, y, false)
				released = true
			}
		})
	}

	if *ch9120Flag {
		ch9120 = devices.NewCH9120(c, 1, 18, 19, 17)
		if os.Getenv("RP2350SIM_CH9120_LOG") != "" {
			ch9120.Logger = debugLog()
		}
		ch9120.Attach()
	}

	var bridges []*devices.UARTBridge
	if *serialTCP != "" {
		for spec := range strings.SplitSeq(*serialTCP, ",") {
			uartStr, addr, ok := strings.Cut(spec, ":")
			if !ok || addr == "" {
				log.Warn("serial-tcp: bad spec", slog.String("spec", spec), slog.String("want", "uart:host:port"))
				continue
			}
			idx, err := strconv.Atoi(uartStr)
			if err != nil || idx < 0 || idx > 1 {
				log.Warn("serial-tcp: bad UART", slog.String("uart", uartStr))
				continue
			}
			br := devices.NewUARTBridge(c, idx, addr)
			br.Logger = log
			if err := br.Start(); err != nil {
				log.Warn("serial-tcp: bridge failed", slog.Int("uart", idx), slog.Any("err", err))
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
	wanted := map[int]bool{}
	for s := range strings.SplitSeq(*tracePins, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err == nil {
			wanted[n] = true
		}
	}
	if *tracePins != "" || len(spiBridges) > 0 {
		c.Pins.OnChange = chainPinCallbacks(c.Pins.OnChange, func(pin int, level bool) {
			if wanted[pin] {
				log.Info("pin change",
					slog.Int("pin", pin), slog.Bool("level", level),
					slog.Float64("ms", float64(c.NowMicros())/1000))
			}
			for _, br := range spiBridges {
				br.SetCS(pin, level)
			}
		})
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
			attrs := append([]any{
				slog.String("pc", hex(pc)),
				slog.String("opcode", fmt.Sprintf("%04x", hw1)),
			}, regAttrs(cpu)...)
			log.Info("trace", attrs...)
		}
	}

	var bp uint64
	bpSet := *bpStr != ""
	if bpSet {
		addr, err := resolveAddress(*bpStr, dbgInfo)
		if err != nil {
			log.Error("breakpoint", slog.Any("err", err))
			exit(2)
		}
		bp = uint64(addr)
	}
	var bp2 uint64
	bp2Set := *bp2Str != ""
	if bp2Set {
		addr, err := resolveAddress(*bp2Str, dbgInfo)
		if err != nil {
			log.Error("secondary breakpoint", slog.Any("err", err))
			exit(2)
		}
		bp2 = uint64(addr)
	}
	var ring []string
	zeros := 0
	bpHits := 0
	if *traceLast || bpSet || bp2Set || *regStr != "" || *stopOnMMIO {
		prevTrace := c.CPU0.TracePC
		watchReg := -1
		lastReg := uint32(0)
		if *regStr != "" {
			n, err := strconv.Atoi(*regStr)
			if err != nil || n < 0 || n > 15 {
				log.Error("-watch-reg needs a register number 0..15", slog.String("value", *regStr))
				exit(2)
			}
			watchReg = n
		}
		maxAddr := uint32(0)
		if img != nil {
			maxAddr = img.MaxAddr()
		}
		c.CPU0.TracePC = func(cpu *m33.CPU, pc uint32, hw1 uint16) {
			if prevTrace != nil {
				prevTrace(cpu, pc, hw1)
			}
			if img != nil && pc >= maxAddr && pc < rp2350.FlashBase+rp2350.FlashSize {
				log.Error("pc outside image",
					slog.String("pc", hex(pc)), slog.String("lr", hex(cpu.R[14])), slog.String("sp", hex(cpu.R[13])))
				start := 0
				if len(ring) > 40 {
					start = len(ring) - 40
				}
				for _, l := range ring[start:] {
					log.Info("trace back", slog.String("line", l))
				}
				c.Halted = true
				exit(1)
			}
			if watchReg >= 0 {
				if v := cpu.R[watchReg]; v != lastReg {
					log.Info("register change",
						slog.Int("reg", watchReg),
						slog.String("from", hex(lastReg)),
						slog.String("to", hex(v)),
						slog.String("pc", hex(pc)),
						slog.String("opcode", fmt.Sprintf("%04x", hw1)))
					lastReg = v
				}
			}
			if bp2Set && uint64(pc) == bp2 {
				attrs := append([]any{slog.String("pc", hex(pc))}, regAttrs(cpu)...)
				log.Info("breakpoint", attrs...)
			}
			if bpSet && uint64(pc) == bp {
				if *msgFlag {
					p, n := c.Read32(cpu.R[1]), c.Read32(cpu.R[1]+4)
					b := make([]byte, 0, n)
					for i := uint32(0); i < n && i < 200; i++ {
						b = append(b, c.Read8(p+i))
					}
					log.Info("runtime message", slog.String("text", string(b)))
				}
				attrs := append([]any{slog.String("pc", hex(pc))}, regAttrs(cpu)...)
				if dbgInfo != nil {
					if fn, ok := dbgInfo.FunctionAt(uint32(pc)); ok {
						attrs = append(attrs, slog.String("symbol", fn.Name))
					}
					if file, line, ok := dbgInfo.LineAt(uint32(pc)); ok {
						attrs = append(attrs, slog.String("file", file), slog.Int("line", line))
					}
				}
				log.Info("break", attrs...)
				bpHits++
				if bpHits >= 200 {
					c.Halted = true
					exit(1)
				}
				return
			}
			if !*traceLast && !*stopOnMMIO {
				return
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
					log.Info("trace back", slog.String("line", l))
				}
				c.Halted = true
				exit(1)
			}
		}
	}

	for kv := range strings.SplitSeq(*pokeStr, ",") {
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
				log.Info("write value match",
					slog.String("addr", hex(addr)), slog.String("value", hex(val)),
					slog.Int("size", size),
					slog.String("pc", hex(c.CPU0.R[15])), slog.String("lr", hex(c.CPU0.R[14])))
			}
		}
	}

	if *watchStr != "" {
		wa, err := resolveAddress(*watchStr, dbgInfo)
		if err != nil {
			log.Error("watchpoint", slog.Any("err", err))
			exit(2)
		}
		prevWrite := c.OnWrite
		c.OnWrite = func(addr uint32, val uint32, size int) {
			if prevWrite != nil {
				prevWrite(addr, val, size)
			}
			if addr == wa {
				log.Info("watched write",
					slog.String("addr", hex(addr)), slog.String("value", hex(val)),
					slog.Int("size", size),
					slog.String("pc", hex(c.CPU0.R[15])), slog.String("lr", hex(c.CPU0.R[14])))
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
			log.Error("unmapped access",
				slog.String("op", op), slog.String("addr", hex(addr)),
				slog.String("value", hex(val)), slog.String("pc", hex(c.CPU0.R[15])))
			for _, l := range ring {
				log.Info("trace back", slog.String("line", l))
			}
		}
	}

	// Console input: USB CDC is flow-controlled by the emulated host, UART0 is
	// queue-safe, so stdin and -input feed both without disturbing the CPU.
	var usbPending []byte
	flushUSBLocked := func() {
		for len(usbPending) > 0 && c.USBCDCReady() {
			n := min(len(usbPending), 64)
			c.SendUSBCDC(usbPending[:n])
			usbPending = usbPending[n:]
		}
	}
	flushUSB := func() {
		debugMu.Lock()
		defer debugMu.Unlock()
		flushUSBLocked()
	}
	queueConsole := func(data []byte) {
		debugMu.Lock()
		defer debugMu.Unlock()
		c.SendUART(0, data)
		usbPending = append(usbPending, data...)
		if len(usbPending) > 8192 {
			dropped := len(usbPending) - 4096
			log.Warn("USB console input queue full, dropping oldest bytes", slog.Int("dropped", dropped))
			usbPending = append([]byte(nil), usbPending[len(usbPending)-4096:]...)
		}
		flushUSBLocked()
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
		n := min(len(stdinBuf), 64)
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
			log.Error("input file", slog.String("path", *inputFile), slog.Any("err", err))
			exit(1)
		}
		inputData = append(inputData, b...)
	}
	inputCycles := uint64(0)
	if *inputAt > 0 {
		inputCycles = uint64(float64(*inputAt) / float64(time.Second) * 150_000_000)
	}

	if logicCap != nil {
		logicCap.attach(c)
	}
	var audioDec *devices.AudioDecoder
	if *audioOut != "" || *audioI2S != "" || *audioPDM != "" || *audioPWM >= 0 {
		if *audioOut == "" {
			log.Error("-audio-i2s/-audio-pdm/-audio-pwm need -audio-out")
			exit(2)
		}
		var audioErr error
		audioDec, audioErr = newAudioDecoder(c, *audioI2S, *audioPDM, *audioPWM, *audioRate)
		if audioErr != nil {
			log.Error("audio", slog.Any("err", audioErr))
			exit(2)
		}
		c.CycleHooks = append(c.CycleHooks, audioDec.Tick)
	}
	var midiBr *midiBridge
	if *usbMIDI || *midiUART >= 0 || *midiUDP != "" || *midiMonitor {
		midiBr = newMIDIBridge(c, *usbMIDI, *midiUART, *midiMonitor, log)
		if *midiUDP != "" {
			if err := midiBr.listenUDP(*midiUDP); err != nil {
				log.Error("midi udp", slog.Any("err", err))
				exit(2)
			}
		}
	}
	if *keepAfter {
		c.ContinueAfterHalt = true
		*allowHalt = true
	}
	netLog := &networkReporter{log: log}
	if *gdbAddr != "" {
		c.Debug = rp2350.NewDebugger()
		tgt := &gdbTarget{chip: c, mu: &debugMu}
		ln, err := net.Listen("tcp", *gdbAddr)
		if err != nil {
			log.Error("gdb listen", slog.String("addr", *gdbAddr), slog.Any("err", err))
			exit(2)
		}
		srv := gdb.NewServer(tgt, func(msg string, args ...any) {
			if !*quiet {
				log.Info(msg, args...)
			}
		})
		gdbSrv = srv
		gdbDone = make(chan struct{})
		go func() {
			defer close(gdbDone)
			if err := srv.Serve(ln); err != nil {
				log.Error("gdb server", slog.Any("err", err))
			}
		}()
		if !*quiet {
			log.Info("gdb server listening", slog.String("addr", ln.Addr().String()))
		}
	}
	start := time.Now()
	timedOut := false
	if *sampleN == 0 {
		runCycles := uint64(*steps)
		if *secs > 0 {
			runCycles = uint64(*secs * 150_000_000)
		}
		if *gdbAddr != "" && !explicitBudget {
			// A debugger session is usually longer than the default 50M-cycle
			// budget; run like a real board until the user stops or detaches.
			runCycles = 1 << 62
		}
		const chunk = 1_000_000
		if len(inputData) > 0 {
			warmLeft := min(inputCycles, runCycles)
			var warmed uint64
			for warmLeft > 0 {
				n := min(warmLeft, chunk)
				debugMu.Lock()
				ran := c.Run(n)
				debugMu.Unlock()
				if ran == 0 {
					break // a GDB halt parks the warm-up without consuming budget
				}
				warmed += ran
				warmLeft -= ran
			}
			runCycles -= warmed
			flushUSB()
			if *stdinFlag {
				drainStdin()
			}
			queueConsole(inputData)
		}
		for runCycles > 0 {
			if *stopExpect && expects.all {
				break
			}
			if *wallLimit > 0 && time.Since(start) > *wallLimit {
				timedOut = true
				break
			}
			n := min(runCycles, chunk)
			debugMu.Lock()
			if c.CPU0.Halted || c.CPU1.Halted || c.Halted {
				debugMu.Unlock()
				break
			}
			done := c.Run(n)
			if midiBr != nil {
				midiBr.tick()
			}
			stopped := c.Debug != nil && c.Debug.Stopped
			boots := c.BootCount()
			var resetReason string
			var resetEntry uint32
			if boots != lastBoot {
				resetReason = c.LastResetReason()
				resetEntry = c.BootEntry()
			}
			debugMu.Unlock()
			runCycles -= done
			netLog.report(w5500, w6300, ch9120)
			if boots != lastBoot {
				lastBoot = boots
				log.Info("reset",
					slog.Uint64("boot", boots),
					slog.String("reason", resetReason),
					slog.String("entry", hex(resetEntry)))
				saveFlash()
			}
			flushUSB()
			if *stdinFlag {
				drainStdin()
			}
			if stopped && done == 0 {
				// The GDB client is in control; poll cheaply instead of
				// spinning the interpreter while it is stopped.
				time.Sleep(2 * time.Millisecond)
			}
		}
		flushUSB()
	} else {
		if len(inputData) > 0 {
			warm := min(inputCycles, uint64(*sampleN)*10_000_000)
			if warm > 0 {
				debugMu.Lock()
				c.Run(warm)
				debugMu.Unlock()
				flushUSB()
				if *stdinFlag {
					drainStdin()
				}
			}
			queueConsole(inputData)
		}
		for i := 0; i < *sampleN; i++ {
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
			c.Run(10_000_000)
			if midiBr != nil {
				midiBr.tick()
			}
			netLog.report(w5500, w6300, ch9120)
			if b := c.BootCount(); b != lastBoot {
				lastBoot = b
				log.Info("reset",
					slog.Uint64("boot", b),
					slog.String("reason", c.LastResetReason()),
					slog.String("entry", hex(c.BootEntry())))
				saveFlash()
			}
			log.Info("sample",
				slog.Int("index", i),
				slog.String("pc", hex(c.CPU0.R[15])), slog.String("lr", hex(c.CPU0.R[14])),
				slog.String("sp", hex(c.CPU0.R[13])),
				slog.String("r0", hex(c.CPU0.R[0])), slog.String("r1", hex(c.CPU0.R[1])))
			flushUSB()
			if *stdinFlag {
				drainStdin()
			}
		}
	}
	if gdbSrv != nil {
		if err := gdbSrv.Close(); err != nil {
			log.Warn("gdb close", slog.Any("err", err))
		}
		<-gdbDone
	}
	netLog.report(w5500, w6300, ch9120)
	saveFlash()
	logicPath := ""
	logicSamples := 0
	if logicCap != nil {
		if writeTrace {
			var logicErr error
			logicPath, logicErr = logicCap.write(*logicOut)
			if logicErr != nil {
				log.Error("logic trace", slog.String("path", logicPath), slog.Any("err", logicErr))
			} else {
				logicSamples = logicCap.samples
				log.Info("logic trace saved",
					slog.String("path", logicPath),
					slog.Int("samples", logicSamples))
			}
		}
		if *vcdOut != "" {
			vcdPath, err := logicCap.writeVCD(*vcdOut)
			if err != nil {
				log.Error("vcd", slog.String("path", vcdPath), slog.Any("err", err))
			} else {
				log.Info("vcd saved", slog.String("path", vcdPath))
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
		log.Error("wall timeout", slog.Duration("after", *wallLimit))
	}
	if len(missing) > 0 {
		log.Error("missing expected output", slog.Any("missing", missing))
	}

	if !*quiet {
		log.Info("run finished",
			slog.Uint64("cycles", c.Cycles),
			slog.Float64("virtual_seconds", float64(c.Cycles)/150e6),
			slog.Float64("wall_seconds", wall.Seconds()))
		log.Info("core0",
			slog.String("pc", hex(c.CPU0.R[15])), slog.String("sp", hex(c.CPU0.R[13])), slog.String("lr", hex(c.CPU0.R[14])))
		log.Info("core1",
			slog.String("pc", hex(c.CPU1.R[15])), slog.String("sp", hex(c.CPU1.R[13])),
			slog.String("lr", hex(c.CPU1.R[14])), slog.Bool("parked", c.CPU1.Parked))
		log.Info("runtime",
			slog.Uint64("irqs", c.IRQCount),
			slog.Uint64("sio_pushes", c.SIOPushes()),
			slog.Uint64("core1_launches", c.Core1Launches()))
		// These are TinyGo task-scheduler internals for the pico2 runtime; the
		// absolute addresses are version-specific and meaningless for C images.
		log.Debug("runqueue",
			slog.String("head", hex(c.Read32(0x200017c0))),
			slog.String("tail", hex(c.Read32(0x200017c4))),
			slog.String("sleep", hex(c.Read32(0x2000179c))),
			slog.String("current", hex(c.Read32(0x2000175c))),
			slog.Uint64("main_exited", uint64(c.Read32(0x20001758))))
	}
	if c.CPU0.Halted {
		log.Error("core0 halted",
			slog.String("reason", c.CPU0.HaltReason),
			slog.String("pc", hex(c.CPU0.R[15])), slog.String("lr", hex(c.CPU0.R[14])), slog.String("sp", hex(c.CPU0.R[13])))
	}
	if c.CPU1.Halted {
		log.Error("core1 halted",
			slog.String("reason", c.CPU1.HaltReason),
			slog.String("pc", hex(c.CPU1.R[15])), slog.String("lr", hex(c.CPU1.R[14])), slog.String("sp", hex(c.CPU1.R[13])))
	}
	if lcd != nil && !*quiet {
		log.Info("lcd", slog.Int("pixels", lcd.PixelsWritten()), slog.Bool("on", lcd.On()))
	}
	if card != nil {
		if err := card.Sync(); err != nil {
			log.Warn("sd sync", slog.Any("err", err))
		}
	}
	if !*quiet && (otpSeeded > 0 || c.OTPWrites() > 0) {
		log.Info("otp",
			slog.Int("rows", c.OTPProgrammedRows()),
			slog.Uint64("writes", c.OTPWrites()))
	}
	if *otpOut != "" {
		if err := dumpOTP(c, *otpOut); err != nil {
			log.Error("otp dump", slog.String("path", *otpOut), slog.Any("err", err))
			if exitCode == 0 {
				exitCode = 1
			}
		}
	}
	if *sensorsOut {
		attrs := []any{}
		if touch != nil {
			tx, ty, pressed := touch.Last()
			attrs = append(attrs, "touch", fmt.Sprintf("%d,%d", tx, ty), "pressed", pressed)
		}
		if imu != nil {
			ax, ay, az := imu.Accel()
			attrs = append(attrs, "accel", fmt.Sprintf("%.2f,%.2f,%.2f", ax, ay, az))
			attrs = append(attrs, "temp", imu.Temperature())
		}
		if card != nil {
			attrs = append(attrs, "sd_blocks", card.Blocks(), "sd_source", card.Source())
		}
		if *touchLCD2 {
			attrs = append(attrs, "battery", 3.9, "backlight", c.PinBrightness(15))
		}
		log.Info("sensors", attrs...)
	}
	if *dviPNG != "" {
		if img := c.HSTXFrame(); img == nil {
			log.Error("dvi png: no frame captured", slog.String("path", *dviPNG))
		} else if f, err := os.Create(*dviPNG); err != nil {
			log.Error("dvi png", slog.String("path", *dviPNG), slog.Any("err", err))
		} else {
			err := png.Encode(f, img)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				log.Error("dvi png", slog.String("path", *dviPNG), slog.Any("err", err))
			} else if !*quiet {
				log.Info("dvi frame saved", slog.String("path", *dviPNG),
					slog.Int("width", img.Bounds().Dx()), slog.Int("height", img.Bounds().Dy()))
			}
		}
	}
	if lcd != nil && *lcdPNG != "" {
		if err := lcd.WritePNG(*lcdPNG); err != nil {
			log.Error("lcd png", slog.String("path", *lcdPNG), slog.Any("err", err))
		}
	}
	if audioDec != nil && *audioOut != "" {
		if err := audioDec.WriteWAV(*audioOut); err != nil {
			log.Error("audio wav", slog.String("path", *audioOut), slog.Any("err", err))
		} else if !*quiet {
			log.Info("audio saved", slog.String("path", *audioOut), slog.Int("samples", audioDec.Len()))
		}
	}
	if midiBr != nil {
		midiBr.close()
	}

	if *peekStr != "" {
		a, _ := strconv.ParseUint(strings.TrimPrefix(*peekStr, "0x"), 16, 32)
		log.Info("memory", slog.String("addr", hex(uint32(a))), slog.String("value", hex(c.Read32(uint32(a)))))
	}

	if *unhandled {
		log.Info("unmapped accesses", slog.String("report", c.DescribeUnhandled()))
	}

	if *summaryOut != "" {
		sum := struct {
			Firmware       string                `json:"firmware"`
			Format         string                `json:"format"`
			Cycles         uint64                `json:"cycles"`
			VirtualSeconds float64               `json:"virtual_seconds"`
			WallSeconds    float64               `json:"wall_seconds"`
			Halted         bool                  `json:"halted"`
			HaltReason     string                `json:"halt_reason,omitempty"`
			Core0PC        uint32                `json:"core0_pc"`
			Core0SP        uint32                `json:"core0_sp"`
			Core1PC        uint32                `json:"core1_pc"`
			Core1SP        uint32                `json:"core1_sp"`
			Core1Parked    bool                  `json:"core1_parked"`
			IRQs           uint64                `json:"irqs"`
			Core1Launches  uint64                `json:"core1_launches"`
			ExpectMatched  bool                  `json:"expectations_matched"`
			ExpectMissing  []string              `json:"missing_expectations,omitempty"`
			TimedOut       bool                  `json:"wall_timed_out"`
			ExitCode       int                   `json:"exit_code"`
			Network        []devices.NetworkInfo `json:"network,omitempty"`
			FlashPath      string                `json:"flash_path,omitempty"`
			LogicPath      string                `json:"logic_trace,omitempty"`
			Boots          uint64                `json:"boots"`
			FlashWrites    uint64                `json:"flash_writes"`
			ResetReason    string                `json:"reset_reason,omitempty"`
			BootEntry      uint32                `json:"boot_entry,omitempty"`
			PSRAMBytes     uint32                `json:"psram_bytes,omitempty"`
			PSRAMLatency   uint32                `json:"psram_latency_cycles,omitempty"`
		}{
			Firmware:       *path,
			Format:         fwFormat,
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
			Network:        netLog.infos(w5500, w6300, ch9120),
			FlashPath:      flashPath,
			LogicPath:      logicPath,
			Boots:          c.BootCount(),
			FlashWrites:    c.FlashWrites(),
			ResetReason:    c.LastResetReason(),
			BootEntry:      c.BootEntry(),
			PSRAMBytes:     c.PSRAMSize(),
			PSRAMLatency:   c.PSRAMLatency(),
		}
		if c.CPU0.Halted {
			sum.HaltReason = c.CPU0.HaltReason
		} else if c.CPU1.Halted {
			sum.HaltReason = c.CPU1.HaltReason
		}
		b, _ := json.MarshalIndent(sum, "", "  ")
		b = append(b, '\n')
		if err := os.WriteFile(*summaryOut, b, 0o644); err != nil {
			log.Error("summary file", slog.String("path", *summaryOut), slog.Any("err", err))
			if exitCode == 0 {
				exitCode = 1
			}
		}
	}

	if exitCode != 0 {
		exit(exitCode)
	}
}
