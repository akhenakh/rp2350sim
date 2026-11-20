package rp2350

// Flash is modelled as a 16 MiB NOR array. Erased cells read 0xFF and page
// programming can only clear bits; the host image loader is allowed to write
// arbitrary bytes when it lays down a firmware image.
const flashErased = 0xFF

// LoadFlashImage replaces the whole array, for a host-provided flash backing
// file. It resets the programming counters because this is not a firmware
// update.
func (c *Chip) LoadFlashImage(data []byte) {
	for i := range c.Flash {
		c.Flash[i] = flashErased
	}
	copy(c.Flash, data)
	c.flashWrites = 0
}

// FlashCopy returns a snapshot of the flash array.
func (c *Chip) FlashCopy() []byte {
	out := make([]byte, len(c.Flash))
	copy(out, c.Flash)
	return out
}

// FlashWrites reports erase/program operations performed by firmware.
func (c *Chip) FlashWrites() uint64 { return c.flashWrites }

func (c *Chip) flashChanged() {
	c.flashWrites++
}

// eraseFlashRange fills a byte range with 0xFF. Offsets are relative to the
// start of flash, matching the RP2350 flash_range_erase ROM service.
func (c *Chip) eraseFlashRange(offset, count uint32) bool {
	if offset > uint32(len(c.Flash)) || count > uint32(len(c.Flash))-offset {
		return false
	}
	changed := false
	for i := range count {
		if c.Flash[offset+i] != flashErased {
			c.Flash[offset+i] = flashErased
			changed = true
		}
	}
	if changed {
		c.flashChanged()
	}
	return true
}

// programFlashRange ANDs bytes into flash, the only transition real flash
// allows (1 -> 0).
func (c *Chip) programFlashRange(offset uint32, data []byte) bool {
	if offset > uint32(len(c.Flash)) || uint32(len(data)) > uint32(len(c.Flash))-offset {
		return false
	}
	changed := false
	for i, b := range data {
		o := offset + uint32(i)
		v := c.Flash[o] & b
		if v != c.Flash[o] {
			c.Flash[o] = v
			changed = true
		}
	}
	if changed {
		c.flashChanged()
	}
	return true
}
