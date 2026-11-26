package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akhenakh/rp2350sim/internal/devices"
	"gopkg.in/yaml.v3"
)

//go:embed boards.yaml
var embeddedBoards []byte

// BoardDef describes one selectable board and the devices attached to it.
type BoardDef struct {
	ID          string          `yaml:"id"`
	Name        string          `yaml:"name"`
	Description string          `yaml:"description"`
	LCD         *LCDDevice      `yaml:"lcd"`
	Touch       *TouchDevice    `yaml:"touch"`
	IMU         *IMUDevice      `yaml:"imu"`
	Battery     *BatteryDevice  `yaml:"battery"`
	SD          *SDDevice       `yaml:"sd"`
	Ethernet    *EthDevice      `yaml:"ethernet"`
	WS2812      *LEDDevice      `yaml:"ws2812"`
	PSRAM       *PSRAMDevice    `yaml:"psram"`
	Analog      []SignalDef     `yaml:"analog"`
	Inputs      []InputDef      `yaml:"inputs"`
	Scope       []int           `yaml:"scope"`
	Logic       *LogicDevice    `yaml:"logic"`
	Serial      []SerialDevice  `yaml:"serial"`
	I2CTCP      []I2CTCPBridge  `yaml:"i2c_tcp"`
	SPITCP      []SPITCPBridge  `yaml:"spi_tcp"`
	I2CPresets  []I2CPresetDef  `yaml:"i2c_presets"`
	SPIPresets  []SPIPresetDef  `yaml:"spi_presets"`
	I2CPlugins  []I2CPluginDef  `yaml:"i2c_plugins"`
	SPIPlugins  []SPIPluginDef  `yaml:"spi_plugins"`
	UARTPlugins []UARTPluginDef `yaml:"serial_plugins"`
	Wifi        *WifiDevice     `yaml:"wifi"`
	Audio       *AudioDevice    `yaml:"audio"`
	MIDI        *MIDIDevice     `yaml:"midi"`
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

// I2CPresetDef attaches a built-in I2C device preset. Addr 0 selects the
// model's default address.
type I2CPresetDef struct {
	Bus    int    `yaml:"bus"`
	Preset string `yaml:"preset"`
	Addr   int    `yaml:"addr"`
}

// SPIPresetDef attaches a built-in SPI device preset on one chip select.
type SPIPresetDef struct {
	Bus    int    `yaml:"bus"`
	CS     int    `yaml:"cs"`
	Preset string `yaml:"preset"`
}

// PluginDef is the common part of a scripted device model. File is the
// desktop path; URL is the browser source. Config is passed to the plugin.
type PluginDef struct {
	Name   string         `yaml:"name"`
	File   string         `yaml:"file"`
	URL    string         `yaml:"url"`
	Config map[string]any `yaml:"config"`
}

// I2CPluginDef attaches a plugin at one 7-bit address.
type I2CPluginDef struct {
	PluginDef `yaml:",inline"`
	Bus       int `yaml:"bus"`
	Addr      int `yaml:"addr"`
}

// SPIPluginDef attaches a plugin on one SPI chip select.
type SPIPluginDef struct {
	PluginDef `yaml:",inline"`
	Bus       int `yaml:"bus"`
	CS        int `yaml:"cs"`
}

// UARTPluginDef attaches a serial device model to one hardware UART.
type UARTPluginDef struct {
	PluginDef `yaml:",inline"`
	UART      int            `yaml:"uart"`
	Pins      map[string]int `yaml:"pins"`
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
		spiCS := map[int]spiPin{}
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
			if err := validSPI(spi, spiCS, d.SPI, d.CS, "lcd"); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
			if d.Width <= 0 || d.Height <= 0 {
				return nil, fmt.Errorf("boards: %s: lcd needs positive width and height", b.ID)
			}
			if d.Rotation != 0 && d.Rotation != 90 && d.Rotation != 180 && d.Rotation != 270 {
				return nil, fmt.Errorf("boards: %s: lcd rotation must be 0, 90, 180 or 270", b.ID)
			}
			if d.DC < 0 || d.RST < 0 {
				return nil, fmt.Errorf("boards: %s: lcd needs dc and rst pins", b.ID)
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
			if d.Rotation != 0 && d.Rotation != 90 && d.Rotation != 180 && d.Rotation != 270 {
				return nil, fmt.Errorf("boards: %s: touch rotation must be 0, 90, 180 or 270", b.ID)
			}
			if d.INT < 0 {
				return nil, fmt.Errorf("boards: %s: touch needs an interrupt pin", b.ID)
			}
			if lcd := b.LCD; lcd != nil {
				w, h := lcd.Width, lcd.Height
				if lcd.Rotation == 90 || lcd.Rotation == 270 {
					w, h = h, w
				}
				if d.Width != w || d.Height != h {
					return nil, fmt.Errorf("boards: %s: touch geometry %dx%d does not match native LCD %dx%d", b.ID, d.Width, d.Height, w, h)
				}
			}
		}
		if d := b.IMU; d != nil {
			if d.Bus != 0 && d.Bus != 1 {
				return nil, fmt.Errorf("boards: %s: imu bus must be 0 or 1", b.ID)
			}
			if err := addI2C(d.Bus, d.Addr, "imu"); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
			if d.INT < 0 {
				return nil, fmt.Errorf("boards: %s: imu needs an interrupt pin", b.ID)
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
				if err := validSPI(spi, spiCS, d.SPI, d.CS, "ethernet"); err != nil {
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
			if err := validSPI(spi, spiCS, d.SPI, d.CS, "sd"); err != nil {
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
			audioPins := map[int]bool{}
			addAudioPin := func(p int) error {
				if !pinOK(p) {
					return fmt.Errorf("invalid GPIO %d", p)
				}
				if audioPins[p] {
					return fmt.Errorf("audio pins must be distinct")
				}
				audioPins[p] = true
				return nil
			}
			var pins []int
			switch d.Kind {
			case "i2s":
				pins = []int{d.BCLK, d.LRCLK, d.Data}
			case "pdm":
				pins = []int{d.Data, d.Clock}
			case "pwm":
				pins = []int{d.Pin}
			default:
				return nil, fmt.Errorf("boards: %s: unknown audio kind %q", b.ID, d.Kind)
			}
			for _, pin := range pins {
				if err := addAudioPin(pin); err != nil {
					return nil, fmt.Errorf("boards: %s: audio %s: %w", b.ID, d.Kind, err)
				}
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
		case "":
			b.Package = "qfn60"
		case "qfn60", "qfn80":
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
			if err := validSPI(spi, spiCS, br.Bus, br.CS, "spi_tcp"); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
			if br.Listen != "" && br.Connect != "" {
				return nil, fmt.Errorf("boards: %s: spi_tcp listen and connect are mutually exclusive", b.ID)
			}
		}
		for i := range b.I2CPresets {
			p := &b.I2CPresets[i]
			info, ok := devices.FindBusPreset(p.Preset)
			if !ok {
				return nil, fmt.Errorf("boards: %s: unknown i2c preset %q", b.ID, p.Preset)
			}
			if info.Kind != devices.BusI2C {
				return nil, fmt.Errorf("boards: %s: preset %q is not an I2C device", b.ID, p.Preset)
			}
			p.Preset = info.Name
			if p.Bus != 0 && p.Bus != 1 {
				return nil, fmt.Errorf("boards: %s: i2c preset %s bus must be 0 or 1", b.ID, p.Preset)
			}
			if p.Addr == 0 {
				p.Addr = int(info.Addresses[0])
			}
			supported := false
			for _, a := range info.Addresses {
				if p.Addr == int(a) {
					supported = true
					break
				}
			}
			if !supported {
				return nil, fmt.Errorf("boards: %s: preset %s does not implement address %#x", b.ID, p.Preset, p.Addr)
			}
			if err := addI2C(p.Bus, p.Addr, "preset "+p.Preset); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
		}
		for i := range b.SPIPresets {
			p := &b.SPIPresets[i]
			info, ok := devices.FindBusPreset(p.Preset)
			if !ok {
				return nil, fmt.Errorf("boards: %s: unknown spi preset %q", b.ID, p.Preset)
			}
			if info.Kind != devices.BusSPI {
				return nil, fmt.Errorf("boards: %s: preset %q is not an SPI device", b.ID, p.Preset)
			}
			p.Preset = info.Name
			if err := validSPI(spi, spiCS, p.Bus, p.CS, "spi preset "+p.Preset); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
		}
		for i := range b.I2CPlugins {
			d := &b.I2CPlugins[i]
			if d.Bus != 0 && d.Bus != 1 {
				return nil, fmt.Errorf("boards: %s: i2c plugin bus must be 0 or 1", b.ID)
			}
			if d.Addr < 0x08 || d.Addr > 0x77 {
				return nil, fmt.Errorf("boards: %s: i2c plugin address %#x is not 7-bit", b.ID, d.Addr)
			}
			if err := preparePlugin(&d.PluginDef, baseDir); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
			if err := addI2C(d.Bus, d.Addr, "plugin "+d.Name); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
		}
		for i := range b.SPIPlugins {
			d := &b.SPIPlugins[i]
			if err := validSPI(spi, spiCS, d.Bus, d.CS, "plugin "+d.Name); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
			if err := preparePlugin(&d.PluginDef, baseDir); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
		}
		for i := range b.UARTPlugins {
			d := &b.UARTPlugins[i]
			if d.UART < 0 || d.UART > 1 {
				return nil, fmt.Errorf("boards: %s: uart plugin uart must be 0 or 1", b.ID)
			}
			if err := preparePlugin(&d.PluginDef, baseDir); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
			if err := claimUART(d.UART, "plugin "+d.Name); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
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
		pinInPackage := func(pin int) bool { return pin >= 0 && pin <= maxPin }
		if len(b.Scope) > devices.MaxTraceChannels {
			return nil, fmt.Errorf("boards: %s: scope supports at most %d pins", b.ID, devices.MaxTraceChannels)
		}
		seenScope := map[int]bool{}
		for _, pin := range b.Scope {
			if !pinInPackage(pin) || seenScope[pin] {
				return nil, fmt.Errorf("boards: %s: invalid or duplicate scope pin GP%d", b.ID, pin)
			}
			seenScope[pin] = true
		}
		if l := b.Logic; l != nil {
			logicPin := func(pin int, optional bool) error {
				if pin == -1 && optional {
					return nil
				}
				if !pinInPackage(pin) {
					return fmt.Errorf("GP%d is outside the selected package", pin)
				}
				return nil
			}
			if u := l.UART; u != nil {
				if u.Bus != 0 && u.Bus != 1 {
					return nil, fmt.Errorf("boards: %s: logic uart bus must be 0 or 1", b.ID)
				}
				if u.Baud <= 0 {
					return nil, fmt.Errorf("boards: %s: logic uart baud must be positive", b.ID)
				}
				if err := logicPin(u.TX, false); err != nil {
					return nil, fmt.Errorf("boards: %s: logic uart tx: %w", b.ID, err)
				}
				if err := logicPin(u.RX, false); err != nil {
					return nil, fmt.Errorf("boards: %s: logic uart rx: %w", b.ID, err)
				}
			}
			if d := l.I2C; d != nil {
				if d.Bus != 0 && d.Bus != 1 {
					return nil, fmt.Errorf("boards: %s: logic i2c bus must be 0 or 1", b.ID)
				}
				for _, pin := range []int{d.SDA, d.SCL} {
					if err := logicPin(pin, false); err != nil {
						return nil, fmt.Errorf("boards: %s: logic i2c: %w", b.ID, err)
					}
				}
			}
			if d := l.SPI; d != nil {
				if d.Bus != 0 && d.Bus != 1 {
					return nil, fmt.Errorf("boards: %s: logic spi bus must be 0 or 1", b.ID)
				}
				if err := logicPin(d.SCK, false); err != nil {
					return nil, fmt.Errorf("boards: %s: logic spi sck: %w", b.ID, err)
				}
				if err := logicPin(d.MOSI, false); err != nil {
					return nil, fmt.Errorf("boards: %s: logic spi mosi: %w", b.ID, err)
				}
				if err := logicPin(d.MISOPin(), true); err != nil {
					return nil, fmt.Errorf("boards: %s: logic spi miso: %w", b.ID, err)
				}
				if err := logicPin(d.CSPin(), true); err != nil {
					return nil, fmt.Errorf("boards: %s: logic spi cs: %w", b.ID, err)
				}
			}
		}
		devicePins := map[int]string{}
		devicePinErr := ""
		addDevicePin := func(who string, pin int) {
			if pin < 0 {
				return
			}
			if pin > maxPin {
				if devicePinErr == "" {
					devicePinErr = fmt.Sprintf("%s pin GP%d is outside the selected package", who, pin)
				}
				return
			}
			if prev, ok := devicePins[pin]; ok {
				// The Touch-LCD-2 wires the panel and the touch controller to
				// one reset line; both resets legitimately share that GPIO.
				if (prev == "lcd rst" && who == "touch rst") ||
					(prev == "touch rst" && who == "lcd rst") {
					devicePins[pin] = prev + " / " + who
					return
				}
				if devicePinErr == "" {
					devicePinErr = fmt.Sprintf("%s conflicts with %s on GP%d", who, prev, pin)
				}
				return
			}
			devicePins[pin] = who
		}
		if d := b.LCD; d != nil {
			addDevicePin("lcd cs", d.CS)
			addDevicePin("lcd dc", d.DC)
			addDevicePin("lcd rst", d.RST)
			addDevicePin("lcd backlight", d.BacklightPin())
		}
		if d := b.Touch; d != nil {
			addDevicePin("touch int", d.INT)
			addDevicePin("touch rst", d.RSTPin())
		}
		if d := b.IMU; d != nil {
			addDevicePin("imu int", d.INT)
			addDevicePin("imu int2", d.INT2Pin())
		}
		if d := b.SD; d != nil {
			addDevicePin("sd cs", d.CS)
		}
		if d := b.Ethernet; d != nil {
			if d.Kind == "ch9120" {
				addDevicePin("ch9120 cfg0", d.CFG0)
				addDevicePin("ch9120 rst", d.RST)
				addDevicePin("ch9120 tcpcs", d.TCPCS)
			} else {
				addDevicePin("ethernet cs", d.CS)
			}
		}
		if d := b.WS2812; d != nil {
			if d.Pin < 0 {
				return nil, fmt.Errorf("boards: %s: ws2812 needs a pin", b.ID)
			}
			addDevicePin("ws2812", d.Pin)
		}
		if d := b.PSRAM; d != nil {
			addDevicePin("psram cs", d.CS)
		}
		for _, p := range b.SPIPresets {
			addDevicePin("preset "+p.Preset+" cs", p.CS)
		}
		if d := b.Battery; d != nil {
			addDevicePin("battery", d.Pin)
		}
		for i, src := range b.Analog {
			if !pinInPackage(src.Pin) {
				return nil, fmt.Errorf("boards: %s: analog %d pin must be 0..%d", b.ID, i+1, maxPin)
			}
			addDevicePin("analog", src.Pin)
		}
		if d := b.Audio; d != nil {
			var pins []int
			switch d.Kind {
			case "i2s":
				pins = []int{d.BCLK, d.LRCLK, d.Data}
			case "pdm":
				pins = []int{d.Data, d.Clock}
			case "pwm":
				pins = []int{d.Pin}
			}
			for _, pin := range pins {
				if owner, ok := devicePins[pin]; ok {
					return nil, fmt.Errorf("boards: %s: audio pin GP%d conflicts with %s", b.ID, pin, owner)
				}
			}
			switch d.Kind {
			case "i2s":
				addDevicePin("audio bclk", d.BCLK)
				addDevicePin("audio lrclk", d.LRCLK)
				addDevicePin("audio data", d.Data)
			case "pdm":
				addDevicePin("audio data", d.Data)
				addDevicePin("audio clock", d.Clock)
			case "pwm":
				addDevicePin("audio pwm", d.Pin)
			}
		}
		if devicePinErr != "" {
			return nil, fmt.Errorf("boards: %s: %s", b.ID, devicePinErr)
		}
		usedPins := map[int]string{}
		checkPin := func(who string, pin int) error {
			if pin < 0 || pin > maxPin {
				return fmt.Errorf("%s pin must be 0..%d", who, maxPin)
			}
			if owner, ok := devicePins[pin]; ok {
				return fmt.Errorf("%s conflicts with %s on GP%d", who, owner, pin)
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
				if in.Max <= 0 {
					in.Max = 3.3
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
		if bootselCount == 0 {
			// Every Pico-style board has a BOOTSEL button on QSPI_SS; make it
			// available without repeating it in every board entry.
			if inputIDs["bootsel"] {
				return nil, fmt.Errorf("boards: %s: input id \"bootsel\" is reserved", b.ID)
			}
			b.Inputs = append(b.Inputs, InputDef{Kind: "bootsel", ID: "bootsel", Label: "BOOTSEL"})
		}
	}
	return file.Boards, nil
}

// preparePlugin resolves a plugin source and gives it a stable display name.
func preparePlugin(d *PluginDef, baseDir string) error {
	if d.File == "" && d.URL == "" {
		return fmt.Errorf("plugin needs file or url")
	}
	if d.File != "" {
		d.File = expandPath(d.File)
		if !filepath.IsAbs(d.File) && baseDir != "" {
			d.File = filepath.Join(baseDir, d.File)
		}
	}
	if d.Name == "" {
		src := d.File
		if src == "" {
			src = d.URL
		}
		d.Name = filepath.Base(src)
	}
	return nil
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

func validSPI(used map[spiPin]string, csOwners map[int]spiPin, spi, cs int, device string) error {
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
	// A GPIO cannot select a device on two controllers: the pin would be
	// driven by both SPI blocks. Devices sharing a controller keep distinct
	// chip selects and are rejected by the spiPin check above.
	if prev, ok := csOwners[cs]; ok && prev.bus != spi {
		return fmt.Errorf("%s and %s both use GP%d as an SPI chip select (SPI%d and SPI%d)",
			used[prev], device, cs, prev.bus, spi)
	}
	used[key] = device
	csOwners[cs] = key
	return nil
}
