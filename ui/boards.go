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
	ID          string     `yaml:"id"`
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	LCD         *LCDDevice `yaml:"lcd"`
	Ethernet    *EthDevice `yaml:"ethernet"`
	WS2812      *LEDDevice `yaml:"ws2812"`
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
