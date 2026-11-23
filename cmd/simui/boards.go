package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed boards.yaml
var embeddedBoards []byte

// BoardDef describes one selectable board and the devices attached to it.
type BoardDef struct {
	ID          string         `yaml:"id"`
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	LCD         *LCDDevice     `yaml:"lcd"`
	Touch       *TouchDevice   `yaml:"touch"`
	IMU         *IMUDevice     `yaml:"imu"`
	Battery     *BatteryDevice `yaml:"battery"`
	SD          *SDDevice      `yaml:"sd"`
	Ethernet    *EthDevice     `yaml:"ethernet"`
	WS2812      *LEDDevice     `yaml:"ws2812"`
	PSRAM       *PSRAMDevice   `yaml:"psram"`
	Analog      []SignalDef    `yaml:"analog"`
	Inputs      []InputDef     `yaml:"inputs"`
	Scope       []int          `yaml:"scope"`
	Logic       *LogicDevice   `yaml:"logic"`
	Serial      []SerialDevice `yaml:"serial"`
	I2CTCP      []I2CTCPBridge `yaml:"i2c_tcp"`
	SPITCP      []SPITCPBridge `yaml:"spi_tcp"`
	Wifi        *WifiDevice    `yaml:"wifi"`
	Audio       *AudioDevice   `yaml:"audio"`
	MIDI        *MIDIDevice    `yaml:"midi"`
	// Package selects the RP2350 variant: qfn60 (RP2350A, 30 GPIOs) or
	// qfn80 (RP2350B, 48 GPIOs).
	Package string `yaml:"package"`
	// HSTX enables the DVI tab: it decodes the HSTX command stream into a
	// framebuffer for Pico DVI demos.
	HSTX bool `yaml:"hstx"`
}

// MIDIDevice selects the MIDI source for the MIDI tab.
type MIDIDevice struct {
	Kind string `yaml:"kind"` // usb or uart
	UART int    `yaml:"uart"`
}

// AudioDevice selects an audio decoder for the Audio tab and WAV export.
type AudioDevice struct {
	Kind       string `yaml:"kind"` // i2s, pdm or pwm
	BCLK       int    `yaml:"bclk"`
	LRCLK      int    `yaml:"lrclk"`
	Data       int    `yaml:"data"`
	Clock      int    `yaml:"clock"`
	Pin        int    `yaml:"pin"`
	Bits       int    `yaml:"bits"`
	Rate       int    `yaml:"rate"`
	Decimation int    `yaml:"decimation"`
}

// SerialDevice exposes a hardware UART over TCP.
type SerialDevice struct {
	UART   int    `yaml:"uart"`
	Listen string `yaml:"listen"`
}

// I2CTCPBridge exposes one I2C bus as Bus Pirate tokens. Addrs lists the
// 7-bit targets the external device answers.
type I2CTCPBridge struct {
	Bus     int    `yaml:"bus"`
	Addrs   []int  `yaml:"addrs"`
	Listen  string `yaml:"listen"`
	Connect string `yaml:"connect"`
}

// SPITCPBridge exposes one SPI bus + chip select as Bus Pirate tokens.
type SPITCPBridge struct {
	Bus     int    `yaml:"bus"`
	CS      int    `yaml:"cs"`
	Listen  string `yaml:"listen"`
	Connect string `yaml:"connect"`
}

// WifiDevice is the virtual Wi-Fi transport: a host-backed serial netdev.
// The CYW43439 radio itself is not emulated.
type WifiDevice struct {
	UART  int `yaml:"uart"`
	CFG0  int `yaml:"cfg0"`
	RST   int `yaml:"rst"`
	TCPCS int `yaml:"tcpcs"`
}

type LCDDevice struct {
	SPI    int `yaml:"spi"`
	CS     int `yaml:"cs"`
	DC     int `yaml:"dc"`
	RST    int `yaml:"rst"`
	Width  int `yaml:"width"`
	Height int `yaml:"height"`
	// Rotation is the panel's logical rotation (0/90/180/270) used to map
	// pointer input back to native touch coordinates.
	Rotation  int  `yaml:"rotation"`
	Backlight *int `yaml:"backlight"`
}

// BacklightPin returns the backlight GPIO, or -1 when it is not modelled.
func (d *LCDDevice) BacklightPin() int {
	if d.Backlight == nil {
		return -1
	}
	return *d.Backlight
}

// TouchDevice is a capacitive controller on an I2C bus.
type TouchDevice struct {
	Bus      int  `yaml:"bus"`
	Addr     int  `yaml:"addr"`
	INT      int  `yaml:"int"`
	RST      *int `yaml:"rst"`
	Width    int  `yaml:"width"`
	Height   int  `yaml:"height"`
	Rotation int  `yaml:"rotation"`
}

// RSTPin returns the shared reset GPIO, or -1 when it is not wired.
func (d *TouchDevice) RSTPin() int {
	if d.RST == nil {
		return -1
	}
	return *d.RST
}

// IMUDevice is a QMI8658 on an I2C bus.
type IMUDevice struct {
	Bus  int  `yaml:"bus"`
	Addr int  `yaml:"addr"`
	INT  int  `yaml:"int"`
	INT2 *int `yaml:"int2"`
}

// INT2Pin returns the second interrupt GPIO, or -1.
func (d *IMUDevice) INT2Pin() int {
	if d.INT2 == nil {
		return -1
	}
	return *d.INT2
}

// BatteryDevice is a resistor-divider battery monitor on an ADC pin.
type BatteryDevice struct {
	Pin     int     `yaml:"pin"`
	Divider float64 `yaml:"divider"`
	Volts   float64 `yaml:"volts"`
}

// SDDevice is a microSD card on a hardware SPI bus.
type SDDevice struct {
	SPI   int    `yaml:"spi"`
	CS    int    `yaml:"cs"`
	Image string `yaml:"image"`
	// Dir backs the card with a real host directory: files are packed into
	// the image at startup and firmware writes are mirrored back.
	Dir string `yaml:"dir"`
}

type EthDevice struct {
	Kind string `yaml:"kind"` // "w5500", "w6300" or "ch9120"
	SPI  int    `yaml:"spi"`
	CS   int    `yaml:"cs"`

	// CH9120 wiring.
	UART  int `yaml:"uart"`
	CFG0  int `yaml:"cfg0"`
	RST   int `yaml:"rst"`
	TCPCS int `yaml:"tcpcs"`
}

type LEDDevice struct {
	Pin int `yaml:"pin"`
}

// PSRAMDevice is an external QSPI PSRAM on QMI chip select 1. Latency is the
// extra virtual cycles charged per access; 0 makes it as fast as SRAM.
type PSRAMDevice struct {
	SizeMB  int `yaml:"size_mb"`
	CS      int `yaml:"cs"`
	Latency int `yaml:"latency"`
}

// LatencyCycles returns the configured access stall.
func (d *PSRAMDevice) LatencyCycles() int {
	if d.Latency < 0 {
		return 0
	}
	return d.Latency
}

// InputDef is one interactive board control on the Board tab: a momentary
// pushbutton, a slide switch, a rotary encoder, an ADC potentiometer/slider or
// the BOOTSEL button.
type InputDef struct {
	Kind string `yaml:"kind"` // button, switch, encoder, adc, bootsel
	ID   string `yaml:"id"`
	// Label is shown on the control; it defaults to the upper-cased id.
	Label string `yaml:"label"`
	// Pin is the single GPIO of a button, switch or ADC input.
	Pin int `yaml:"pin"`
	// PinA/PinB are the two quadrature lines of an encoder.
	PinA int `yaml:"pin_a"`
	PinB int `yaml:"pin_b"`
	// ActiveLow inverts the electrical level (the default, as on the Pico's
	// BOOTSEL and most breakout buttons).
	ActiveLow *bool `yaml:"active_low"`
	// Initial is the switch position at board load (true = closed/on).
	Initial bool `yaml:"initial"`
	// Volts is the initial ADC voltage; it defaults to mid-scale.
	Volts *float64 `yaml:"volts"`
	// Min, Max and Step configure an ADC slider.
	Min  float64 `yaml:"min"`
	Max  float64 `yaml:"max"`
	Step float64 `yaml:"step"`
}

// IsActiveLow reports whether the input is active low (default true).
func (d *InputDef) IsActiveLow() bool {
	return d.ActiveLow == nil || *d.ActiveLow
}

// DefaultVolts returns the initial ADC voltage (mid-scale when unset).
func (d *InputDef) DefaultVolts() float64 {
	if d.Volts != nil {
		return *d.Volts
	}
	return (d.Min + d.Max) / 2
}

// SignalDef describes a generated analog waveform on a pin (scope demo).
type SignalDef struct {
	Pin       int     `yaml:"pin"`
	Wave      string  `yaml:"wave"`
	Hz        float64 `yaml:"hz"`
	Amplitude float64 `yaml:"amplitude"`
	Offset    float64 `yaml:"offset"`
}

// LogicDevice lists the wire protocols the logic analyzer should decode.
type LogicDevice struct {
	UART *UARTDevice `yaml:"uart"`
	I2C  *I2CDevice  `yaml:"i2c"`
	SPI  *SPIDevice  `yaml:"spi"`
}

type UARTDevice struct {
	Bus  int `yaml:"bus"`
	TX   int `yaml:"tx"`
	RX   int `yaml:"rx"`
	Baud int `yaml:"baud"`
}

type I2CDevice struct {
	Bus int `yaml:"bus"`
	SDA int `yaml:"sda"`
	SCL int `yaml:"scl"`
}

type SPIDevice struct {
	Bus  int  `yaml:"bus"`
	SCK  int  `yaml:"sck"`
	MOSI int  `yaml:"mosi"`
	MISO *int `yaml:"miso"`
	CS   *int `yaml:"cs"`
}

// MISOPin returns the MISO pin, or -1 when the line is not wired.
func (d *SPIDevice) MISOPin() int {
	if d.MISO == nil {
		return -1
	}
	return *d.MISO
}

// CSPin returns the chip-select pin, or -1 when it is not wired.
func (d *SPIDevice) CSPin() int {
	if d.CS == nil {
		return -1
	}
	return *d.CS
}

// LogicPins returns the unique pins the configured protocols observe.
func (b *BoardDef) LogicPins() []int {
	seen := map[int]bool{}
	var out []int
	add := func(p int) {
		if p < 0 || p > 47 || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	if l := b.Logic; l != nil {
		if u := l.UART; u != nil {
			add(u.TX)
			add(u.RX)
		}
		if d := l.I2C; d != nil {
			add(d.SDA)
			add(d.SCL)
		}
		if d := l.SPI; d != nil {
			add(d.SCK)
			add(d.MOSI)
			add(d.MISOPin())
			add(d.CSPin())
		}
	}
	return out
}

// ScopePins returns the default scope channels.
func (b *BoardDef) ScopePins() []int {
	if len(b.Scope) > 0 {
		return append([]int(nil), b.Scope...)
	}
	return []int{0, 1}
}

type boardFile struct {
	Boards []BoardDef `yaml:"boards"`
}

// LoadBoards reads the board list: RP2350SIM_BOARDS when set, otherwise the
// embedded boards.yaml. It validates ids and SPI assignment so a bad file
// fails at startup rather than silently attaching the wrong model.
func LoadBoards() ([]BoardDef, error) {
	data := embeddedBoards
	baseDir := ""
	if p := os.Getenv("RP2350SIM_BOARDS"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("boards: %w", err)
		}
		data = b
		baseDir = filepath.Dir(p)
	}

	var file boardFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("boards: %w", err)
	}
	if len(file.Boards) == 0 {
		return nil, fmt.Errorf("boards: no boards defined")
	}

	ids := map[string]bool{}
	for i := range file.Boards {
		b := &file.Boards[i]
		if b.ID == "" {
			return nil, fmt.Errorf("boards: entry %d has no id", i+1)
		}
		if ids[b.ID] {
			return nil, fmt.Errorf("boards: duplicate id %q", b.ID)
		}
		ids[b.ID] = true
		if b.Name == "" {
			b.Name = b.ID
		}
		spi := map[spiPin]string{}
		var uartOwner [2]string
		claimUART := func(uart int, who string) error {
			if uartOwner[uart] != "" {
				return fmt.Errorf("%s and %s both use UART%d", uartOwner[uart], who, uart)
			}
			uartOwner[uart] = who
			return nil
		}
		i2cAddrs := map[struct {
			bus  int
			addr int
		}]string{}
		addI2C := func(bus, addr int, who string) error {
			if bus != 0 && bus != 1 {
				return fmt.Errorf("%s: bus must be 0 or 1", who)
			}
			if addr < 0x08 || addr > 0x77 {
				return fmt.Errorf("%s: address %#x is not 7-bit", who, addr)
			}
			key := struct {
				bus  int
				addr int
			}{bus, addr}
			if prev, ok := i2cAddrs[key]; ok {
				return fmt.Errorf("%s and %s both use I2C%d address %#x", prev, who, bus, addr)
			}
			i2cAddrs[key] = who
			return nil
		}
		if d := b.LCD; d != nil {
			if err := validSPI(spi, d.SPI, d.CS, "lcd"); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
		}
		if d := b.Touch; d != nil {
			if d.Bus != 0 && d.Bus != 1 {
				return nil, fmt.Errorf("boards: %s: touch bus must be 0 or 1", b.ID)
			}
			if err := addI2C(d.Bus, d.Addr, "touch"); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
			if d.Width <= 0 || d.Height <= 0 {
				return nil, fmt.Errorf("boards: %s: touch needs width and height", b.ID)
			}
		}
		if d := b.IMU; d != nil {
			if d.Bus != 0 && d.Bus != 1 {
				return nil, fmt.Errorf("boards: %s: imu bus must be 0 or 1", b.ID)
			}
			if err := addI2C(d.Bus, d.Addr, "imu"); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
		}
		if d := b.Battery; d != nil {
			if d.Pin < 26 || d.Pin > 29 {
				return nil, fmt.Errorf("boards: %s: battery pin must be an ADC pin (26-29)", b.ID)
			}
			if d.Divider <= 0 {
				d.Divider = 1
			}
			if d.Volts <= 0 {
				d.Volts = 3.9
			}
		}
		if d := b.Ethernet; d != nil {
			switch d.Kind {
			case "":
				d.Kind = "w5500"
			case "w5500", "w6300":
				if err := validSPI(spi, d.SPI, d.CS, "ethernet"); err != nil {
					return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
				}
			case "ch9120":
				if d.UART < 0 || d.UART > 1 {
					return nil, fmt.Errorf("boards: %s: ch9120 uart must be 0 or 1", b.ID)
				}
				if err := claimUART(d.UART, "ethernet ch9120"); err != nil {
					return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
				}
			default:
				return nil, fmt.Errorf("boards: %s: unknown ethernet kind %q", b.ID, d.Kind)
			}
		}
		if d := b.SD; d != nil {
			if err := validSPI(spi, d.SPI, d.CS, "sd"); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
			if d.Image != "" && d.Dir != "" {
				return nil, fmt.Errorf("boards: %s: sd image and dir are mutually exclusive", b.ID)
			}
			if d.Dir != "" {
				d.Dir = expandPath(d.Dir)
				if !filepath.IsAbs(d.Dir) && baseDir != "" {
					d.Dir = filepath.Join(baseDir, d.Dir)
				}
			}
			if d.Image != "" {
				d.Image = expandPath(d.Image)
				if !filepath.IsAbs(d.Image) && baseDir != "" {
					d.Image = filepath.Join(baseDir, d.Image)
				}
			}
		}
		for _, sd := range b.Serial {
			if sd.UART < 0 || sd.UART > 1 {
				return nil, fmt.Errorf("boards: %s: serial uart must be 0 or 1", b.ID)
			}
			// An empty listen address means 127.0.0.1:0 (kernel-chosen port).
		}
		if d := b.Audio; d != nil {
			pinOK := func(p int) bool { return p >= 0 && p <= 47 }
			switch d.Kind {
			case "i2s":
				if !pinOK(d.BCLK) || !pinOK(d.LRCLK) || !pinOK(d.Data) {
					return nil, fmt.Errorf("boards: %s: audio i2s needs bclk, lrclk and data pins", b.ID)
				}
			case "pdm":
				if !pinOK(d.Data) || !pinOK(d.Clock) {
					return nil, fmt.Errorf("boards: %s: audio pdm needs data and clock pins", b.ID)
				}
			case "pwm":
				if !pinOK(d.Pin) {
					return nil, fmt.Errorf("boards: %s: audio pwm needs pin", b.ID)
				}
			default:
				return nil, fmt.Errorf("boards: %s: unknown audio kind %q", b.ID, d.Kind)
			}
			if d.Rate <= 0 {
				d.Rate = 48000
			}
			if d.Bits <= 0 {
				d.Bits = 16
			}
			if d.Bits > 32 {
				return nil, fmt.Errorf("boards: %s: audio bits must be 1..32", b.ID)
			}
			if d.Decimation <= 0 {
				d.Decimation = 64
			}
		}
		if d := b.MIDI; d != nil {
			switch d.Kind {
			case "usb":
			case "uart":
				if d.UART < 0 || d.UART > 1 {
					return nil, fmt.Errorf("boards: %s: midi uart must be 0 or 1", b.ID)
				}
			default:
				return nil, fmt.Errorf("boards: %s: unknown midi kind %q", b.ID, d.Kind)
			}
		}
		switch b.Package {
		case "", "qfn60", "qfn80":
		default:
			return nil, fmt.Errorf("boards: %s: unknown package %q (qfn60 or qfn80)", b.ID, b.Package)
		}
		for i := range b.I2CTCP {
			br := &b.I2CTCP[i]
			if len(br.Addrs) == 0 {
				return nil, fmt.Errorf("boards: %s: i2c_tcp needs at least one address", b.ID)
			}
			if br.Listen != "" && br.Connect != "" {
				return nil, fmt.Errorf("boards: %s: i2c_tcp listen and connect are mutually exclusive", b.ID)
			}
			for _, addr := range br.Addrs {
				if err := addI2C(br.Bus, addr, "i2c_tcp"); err != nil {
					return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
				}
			}
		}
		for _, br := range b.SPITCP {
			if err := validSPI(spi, br.Bus, br.CS, "spi_tcp"); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
			if br.Listen != "" && br.Connect != "" {
				return nil, fmt.Errorf("boards: %s: spi_tcp listen and connect are mutually exclusive", b.ID)
			}
		}
		if w := b.Wifi; w != nil {
			if w.UART < 0 || w.UART > 1 {
				return nil, fmt.Errorf("boards: %s: wifi uart must be 0 or 1", b.ID)
			}
			if err := claimUART(w.UART, "wifi"); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
		}
		if p := b.PSRAM; p != nil {
			if p.SizeMB <= 0 || p.SizeMB > 16 || p.SizeMB&(p.SizeMB-1) != 0 {
				return nil, fmt.Errorf("boards: %s: psram size_mb must be 1, 2, 4, 8 or 16", b.ID)
			}
			if p.CS < 0 || p.CS > 47 {
				return nil, fmt.Errorf("boards: %s: psram cs must be a GPIO number", b.ID)
			}
			if p.Latency < 0 {
				return nil, fmt.Errorf("boards: %s: psram latency must not be negative", b.ID)
			}
		}
		maxPin := 29
		if b.Package == "qfn80" {
			maxPin = 47
		}
		usedPins := map[int]string{}
		checkPin := func(who string, pin int) error {
			if pin < 0 || pin > maxPin {
				return fmt.Errorf("%s pin must be 0..%d", who, maxPin)
			}
			if prev, ok := usedPins[pin]; ok {
				return fmt.Errorf("%s and %s both use GP%d", prev, who, pin)
			}
			usedPins[pin] = who
			return nil
		}
		inputIDs := map[string]bool{}
		bootselCount := 0
		for i := range b.Inputs {
			in := &b.Inputs[i]
			kind := strings.ToLower(strings.TrimSpace(in.Kind))
			switch kind {
			case "pushbutton", "button", "btn":
				kind = "button"
			case "switch", "slide", "toggle":
				kind = "switch"
			case "encoder", "rotary":
				kind = "encoder"
			case "adc", "pot", "potentiometer", "slider":
				kind = "adc"
			case "bootsel", "boot":
				kind = "bootsel"
			default:
				return nil, fmt.Errorf("boards: %s: unknown input kind %q", b.ID, in.Kind)
			}
			in.Kind = kind
			if in.ID == "" {
				return nil, fmt.Errorf("boards: %s: input %d has no id", b.ID, i+1)
			}
			if inputIDs[in.ID] {
				return nil, fmt.Errorf("boards: %s: duplicate input id %q", b.ID, in.ID)
			}
			inputIDs[in.ID] = true
			if in.Label == "" {
				in.Label = strings.ToUpper(in.ID)
			}
			switch kind {
			case "button", "switch":
				if err := checkPin(in.ID, in.Pin); err != nil {
					return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
				}
			case "encoder":
				if in.PinA == in.PinB {
					return nil, fmt.Errorf("boards: %s: encoder %s needs two different pins", b.ID, in.ID)
				}
				if err := checkPin(in.ID+" A", in.PinA); err != nil {
					return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
				}
				if err := checkPin(in.ID+" B", in.PinB); err != nil {
					return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
				}
			case "adc":
				if in.Pin < 26 || in.Pin > 29 {
					return nil, fmt.Errorf("boards: %s: adc %s pin must be 26..29", b.ID, in.ID)
				}
				if err := checkPin(in.ID, in.Pin); err != nil {
					return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
				}
				if in.Max <= in.Min {
					in.Min, in.Max = 0, 3.3
				}
				in.Min = max(in.Min, 0)
				in.Max = min(in.Max, 3.3)
				if in.Max <= in.Min {
					return nil, fmt.Errorf("boards: %s: adc %s range must be inside 0..3.3 V", b.ID, in.ID)
				}
				if in.Step <= 0 {
					in.Step = 0.01
				}
				if in.Volts != nil {
					v := min(max(*in.Volts, in.Min), in.Max)
					in.Volts = &v
				}
			case "bootsel":
				bootselCount++
				if bootselCount > 1 {
					return nil, fmt.Errorf("boards: %s: only one bootsel input is allowed", b.ID)
				}
			}
		}
	}
	return file.Boards, nil
}

// expandPath expands ~ and environment variables in a board path.
func expandPath(p string) string {
	p = os.ExpandEnv(p)
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// spiPin identifies one chip select on an SPI controller. A bus may carry
// several devices as long as each has its own CS.
type spiPin struct {
	bus, cs int
}

func validSPI(used map[spiPin]string, spi, cs int, device string) error {
	if spi != 0 && spi != 1 {
		return fmt.Errorf("%s: spi must be 0 or 1", device)
	}
	if cs < 0 || cs > 47 {
		return fmt.Errorf("%s: cs GP%d is not a valid pin", device, cs)
	}
	key := spiPin{bus: spi, cs: cs}
	if prev, ok := used[key]; ok {
		return fmt.Errorf("%s and %s both use SPI%d CS GP%d", prev, device, spi, cs)
	}
	used[key] = device
	return nil
}
