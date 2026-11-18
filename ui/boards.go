package main

import (
	_ "embed"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

//go:embed boards.yaml
var embeddedBoards []byte

// BoardDef describes one selectable board and the devices attached to it.
type BoardDef struct {
	ID          string       `yaml:"id"`
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	LCD         *LCDDevice   `yaml:"lcd"`
	Ethernet    *EthDevice   `yaml:"ethernet"`
	WS2812      *LEDDevice   `yaml:"ws2812"`
	Analog      []SignalDef  `yaml:"analog"`
	Scope       []int        `yaml:"scope"`
	Logic       *LogicDevice `yaml:"logic"`
}

type LCDDevice struct {
	SPI    int `yaml:"spi"`
	CS     int `yaml:"cs"`
	DC     int `yaml:"dc"`
	RST    int `yaml:"rst"`
	Width  int `yaml:"width"`
	Height int `yaml:"height"`
}

type EthDevice struct {
	Kind string `yaml:"kind"` // "w5500" or "w6300"
	SPI  int    `yaml:"spi"`
	CS   int    `yaml:"cs"`
}

type LEDDevice struct {
	Pin int `yaml:"pin"`
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
	if p := os.Getenv("RP2350SIM_BOARDS"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("boards: %w", err)
		}
		data = b
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
		spi := map[int]string{}
		if d := b.LCD; d != nil {
			if err := validSPI(spi, d.SPI, "lcd"); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
		}
		if d := b.Ethernet; d != nil {
			switch d.Kind {
			case "":
				d.Kind = "w5500"
			case "w5500", "w6300":
			default:
				return nil, fmt.Errorf("boards: %s: unknown ethernet kind %q", b.ID, d.Kind)
			}
			if err := validSPI(spi, d.SPI, "ethernet"); err != nil {
				return nil, fmt.Errorf("boards: %s: %w", b.ID, err)
			}
		}
	}
	return file.Boards, nil
}

func validSPI(used map[int]string, spi int, device string) error {
	if spi != 0 && spi != 1 {
		return fmt.Errorf("%s: spi must be 0 or 1", device)
	}
	if prev, ok := used[spi]; ok {
		return fmt.Errorf("%s and %s both use SPI%d", prev, device, spi)
	}
	used[spi] = device
	return nil
}
