package rp2350

import "fmt"

// spiBlock models the PL022 SPI controllers (SPI0 at 0x40080000, SPI1 at
// 0x40088000). Frames written to the data register are handed to the attached
// device; the returned frames are served back through the receive FIFO.
type spiBlock struct {
	block *regBlock
	// Transfer is called for every byte written. It returns the byte clocked
	// in on MISO (0xFF when nothing is attached).
	Transfer  func(tx byte) (rx byte)
	rx        []uint32
	transfers uint64
	ror       bool
	badDSS    bool
}

const (
	spiSRTFE = 1 << 0
	spiSRTNF = 1 << 1
	spiSRRNE = 1 << 2
	spiSRBSY = 1 << 4

	spiRXDepth = 8 // PL022 receive FIFO depth on the RP2350
)

func (s *spiBlock) reset() {
	s.rx = s.rx[:0]
	s.transfers = 0
	s.ror = false
	s.badDSS = false
}

func newSPI(c *Chip, base uint32, id int) *spiBlock {
	s := &spiBlock{block: newRegBlock(base, 0x1000)}
	s.block.resetFn = func(c *Chip) { s.reset() }
	s.Transfer = func(tx byte) byte {
		if id == 0 && c.OnSPI0 != nil {
			return c.OnSPI0(tx)
		}
		if id == 1 && c.OnSPI1 != nil {
			return c.OnSPI1(tx)
		}
		return 0xFF
	}
	s.block.readFn = func(c *Chip, off uint32) (uint32, bool) {
		switch off {
		case 0x0C: // SSPSR
			sr := uint32(spiSRTFE | spiSRTNF)
			if len(s.rx) > 0 {
				sr |= spiSRRNE
			}
			if len(s.rx) == spiRXDepth {
				sr |= 1 << 3 // RFF
			}
			return sr, true
		case 0x08: // SSPDR
			if len(s.rx) == 0 {
				return 0, true
			}
			v := s.rx[0]
			s.rx = s.rx[1:]
			return v, true
		case 0x18: // SSPRIS
			return s.rawInterruptStatus(), true
		case 0x1c: // SSPMIS
			return s.rawInterruptStatus() & s.block.regs[0x14/4], true
		case 0x20: // SSPICR
			return 0, true
		case 0xfe0:
			return 0x22, true
		case 0xfe4:
			return 0x10, true
		case 0xfe8:
			return 0x34, true
		case 0xfec:
			return 0, true
		case 0xff0:
			return 0x0d, true
		case 0xff4:
			return 0xf0, true
		case 0xff8:
			return 0x05, true
		case 0xffc:
			return 0xb1, true
		}
		return 0, false
	}
	s.block.writeFn = func(c *Chip, off, v uint32) {
		switch off {
		case 0x00: // SSPCR0: DSS selects 4..16 bits
			// The register file keeps the value; nothing else to decode here.
		case 0x08: // SSPDR
			s.transfers++
			dss := int(s.block.regs[0]&0xF) + 1
			var frame uint32
			switch dss {
			case 8:
				frame = uint32(s.Transfer(byte(v)))
			case 16:
				hi := s.Transfer(byte(v >> 8))
				lo := s.Transfer(byte(v))
				frame = uint32(hi)<<8 | uint32(lo)
			default:
				if !s.badDSS {
					s.badDSS = true
					c.Logger.Error("spi: unsupported frame size, treating as 8 bits",
						"dss", dss, "base", fmt.Sprintf("%#x", s.block.base))
				}
				frame = uint32(s.Transfer(byte(v)))
			}
			if len(s.rx) >= spiRXDepth {
				// Hardware drops the incoming frame and raises an overrun.
				s.ror = true
				return
			}
			s.rx = append(s.rx, frame)
		case 0x20: // SSPICR: write-1-to-clear RORIC
			if v&1 != 0 {
				s.ror = false
			}
		}
	}
	return s
}

func (s *spiBlock) rawInterruptStatus() uint32 {
	ris := uint32(1 << 3) // TXRIS: the instantaneous TX FIFO is half empty or less
	if s.ror {
		ris |= 1
	}
	if len(s.rx) >= spiRXDepth/2 {
		ris |= 1 << 2
	}
	return ris
}

// AddSPI attaches a PL022 SPI controller.
func (c *Chip) AddSPI(base uint32, id int) {
	c.addRegBlock(newSPI(c, base, id).block)
}
