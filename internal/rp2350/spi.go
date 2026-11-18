package rp2350

// spiBlock models the PL022 SPI controllers (SPI0 at 0x40080000, SPI1 at
// 0x40088000). Bytes written to the data register are handed to the attached
// device; the returned bytes are served back through the receive FIFO.
type spiBlock struct {
	block *regBlock
	// Transfer is called for every byte written. It returns the byte clocked
	// in on MISO (0xFF when nothing is attached).
	Transfer  func(tx byte) (rx byte)
	rx        []byte
	transfers uint64
}

const (
	spiSRTFE = 1 << 0
	spiSRTNF = 1 << 1
	spiSRRNE = 1 << 2
	spiSRBSY = 1 << 4
)

func newSPI(c *Chip, base uint32, id int) *spiBlock {
	s := &spiBlock{block: newRegBlock(base, 0x1000)}
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
			return sr, true
		case 0x08: // SSPDR
			if len(s.rx) == 0 {
				return 0, true
			}
			v := uint32(s.rx[0])
			s.rx = s.rx[1:]
			return v, true
		}
		return 0, false
	}
	s.block.writeFn = func(c *Chip, off, v uint32) {
		if off == 0x08 { // SSPDR: push a byte through the bus
			s.transfers++
			rx := s.Transfer(byte(v))
			s.rx = append(s.rx, rx)
			if len(s.rx) > 32 {
				s.rx = s.rx[len(s.rx)-32:]
			}
		}
	}
	return s
}

// AddSPI attaches a PL022 SPI controller.
func (c *Chip) AddSPI(base uint32, id int) {
	c.addRegBlock(newSPI(c, base, id).block)
}
