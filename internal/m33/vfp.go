package m33

// VFPv5-SP/DP support is added incrementally; this placeholder keeps the
// decoder honest until the register file and instruction set land.
func (c *CPU) vfpExecute(hw1, hw2 uint16) {
	c.Halt("unimplemented VFP instruction")
}
