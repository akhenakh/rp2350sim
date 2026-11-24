package m33

// Shared types and constants for the differential corpus.

const (
	qemuCodeBase = 0x20000000
	qemuStackTop = 0x20007000
)

type diffCase struct {
	name       string
	hw         []uint16
	regs       [13]uint32
	cpsr       uint32
	mem        map[uint32]uint32 // preloaded words
	steps      int
	expectHalt bool
}

func (c diffCase) bytes() []byte {
	var b []byte
	for _, h := range c.hw {
		b = append(b, byte(h), byte(h>>8))
	}
	return b
}
