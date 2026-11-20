package debug

import (
	"debug/elf"
	"encoding/binary"
)

// cfaRule is one CFA definition valid in [start,end).
type cfaRule struct {
	start, end uint64
	reg        int
	offset     int64
}

type cfaTable struct {
	rules []cfaRule
}

type cfaState struct {
	reg    int
	offset int64
}

// parseCFA reads the small subset of .debug_frame needed to recover the CFA
// for frame-relative locals. It intentionally ignores register save rules.
func parseCFA(f *elf.File) *cfaTable {
	sec := f.Section(".debug_frame")
	if sec == nil {
		return nil
	}
	data, err := sec.Data()
	if err != nil || len(data) < 4 {
		return nil
	}
	t := &cfaTable{}
	cies := map[uint32]cfaState{}
	for off := 0; off+4 <= len(data); {
		length := int(binary.LittleEndian.Uint32(data[off:]))
		if length == 0 {
			break
		}
		if length < 0 || off+4+length > len(data) {
			break
		}
		id := binary.LittleEndian.Uint32(data[off+4:])
		end := off + 4 + length
		if id == 0xffffffff { // CIE
			if st, ok := parseCIE(data[off+8 : end]); ok {
				cies[uint32(off)] = st
			}
		} else if st, ok := cies[id]; ok {
			t.parseFDE(data[off+8:end], st)
		}
		off = end
	}
	return t
}

func parseCIE(b []byte) (cfaState, bool) {
	st := cfaState{reg: 13}
	if len(b) < 4 {
		return st, false
	}
	version := b[0]
	p := 1
	for p < len(b) && b[p] != 0 {
		p++
	}
	p++ // augmentation string
	if p >= len(b) {
		return st, false
	}
	_, p, ok := uleb(b, p) // code alignment
	if !ok {
		return st, false
	}
	if _, p, ok = sleb(b, p); !ok { // data alignment
		return st, false
	}
	if version >= 4 {
		if _, p, ok = uleb(b, p); !ok {
			return st, false
		}
	} else {
		p++
	}
	// Empty augmentation has no extra data. For "z" CIEs, skip the
	// augmentation length and payload before the instruction stream.
	if p > 0 && p <= len(b) && len(b) > 1 && b[1] == 'z' {
		n, next, ok := uleb(b, p)
		if !ok || next+int(n) > len(b) {
			return st, false
		}
		p = next + int(n)
	}
	applyCFAInstructions(b[p:], &st, nil)
	return st, true
}

func (t *cfaTable) parseFDE(b []byte, initial cfaState) {
	if len(b) < 8 {
		return
	}
	start := uint64(binary.LittleEndian.Uint32(b))
	endPC := start + uint64(binary.LittleEndian.Uint32(b[4:]))
	// .debug_frame stores an address range; .eh_frame stores an end PC.
	// The read value is a range here, which is what the CIE above describes.
	st := initial
	p := 8
	// A 'z' CIE would have an augmentation length after the two addresses;
	// the C and TinyGo frames here do not use one.
	pc := start
	t.rules = append(t.rules, cfaRule{start: start, end: endPC, reg: st.reg, offset: st.offset})
	for p < len(b) {
		op := b[p]
		p++
		hi := op & 0xc0
		low := op & 0x3f
		switch hi {
		case 0x40: // advance_loc
			pc += uint64(low) * 2
		case 0x80: // offset: ignore for CFA
		case 0xc0: // restore: ignore
		default:
			switch low {
			case 0x00: // nop
			case 0x01: // set_loc
				if p+4 <= len(b) {
					pc = uint64(binary.LittleEndian.Uint32(b[p:]))
					p += 4
				}
			case 0x02: // advance_loc1
				if p < len(b) {
					pc += uint64(b[p]) * 2
					p++
				}
			case 0x03: // advance_loc2
				if p+2 <= len(b) {
					pc += uint64(binary.LittleEndian.Uint16(b[p:])) * 2
					p += 2
				}
			case 0x04: // advance_loc4
				if p+4 <= len(b) {
					pc += uint64(binary.LittleEndian.Uint32(b[p:])) * 2
					p += 4
				}
			case 0x0c: // def_cfa
				reg, next, ok := uleb(b, p)
				if !ok {
					return
				}
				off, next2, ok := uleb(b, next)
				if !ok {
					return
				}
				st.reg, st.offset = int(reg), int64(off)
				p = next2
				t.add(pc, endPC, st)
			case 0x0d: // def_cfa_register
				reg, next, ok := uleb(b, p)
				if !ok {
					return
				}
				st.reg, p = int(reg), next
				t.add(pc, endPC, st)
			case 0x0e: // def_cfa_offset
				off, next, ok := uleb(b, p)
				if !ok {
					return
				}
				st.offset, p = int64(off), next
				t.add(pc, endPC, st)
			case 0x11: // offset_extended: reg + offset, ignore
				_, next, ok := uleb(b, p)
				if !ok {
					return
				}
				_, next, ok = uleb(b, next)
				if !ok {
					return
				}
				p = next
			case 0x12: // def_cfa_sf
				reg, next, ok := uleb(b, p)
				if !ok {
					return
				}
				off, next2, ok := sleb(b, next)
				if !ok {
					return
				}
				st.reg, st.offset = int(reg), off
				p = next2
				t.add(pc, endPC, st)
			case 0x13: // def_cfa_offset_sf
				off, next, ok := sleb(b, p)
				if !ok {
					return
				}
				st.offset, p = off, next
				t.add(pc, endPC, st)
			default:
				// Unknown instruction: stop rather than desynchronise.
				return
			}
		}
	}
}

func (t *cfaTable) add(pc, end uint64, st cfaState) {
	if len(t.rules) > 0 && t.rules[len(t.rules)-1].start == pc {
		t.rules[len(t.rules)-1] = cfaRule{start: pc, end: end, reg: st.reg, offset: st.offset}
		return
	}
	t.rules = append(t.rules, cfaRule{start: pc, end: end, reg: st.reg, offset: st.offset})
}

func (t *cfaTable) at(low, pc uint32, regs [16]uint32) (uint64, bool) {
	var best *cfaRule
	for n := range t.rules {
		r := &t.rules[n]
		if uint64(pc) < r.start || uint64(pc) >= r.end {
			continue
		}
		if best == nil || r.start > best.start {
			best = r
		}
	}
	if best == nil || best.reg < 0 || best.reg >= len(regs) {
		return 0, false
	}
	return uint64(int64(regs[best.reg]) + best.offset), true
}

func applyCFAInstructions(b []byte, st *cfaState, table *cfaTable) {
	// The CIE only needs its initial CFA rule; advances are irrelevant.
	for p := 0; p < len(b); {
		op := b[p]
		p++
		hi := op & 0xc0
		low := op & 0x3f
		switch hi {
		case 0x40:
		case 0x80:
		case 0xc0:
		default:
			switch low {
			case 0x00:
			case 0x0c:
				reg, next, ok := uleb(b, p)
				if !ok {
					return
				}
				off, next2, ok := uleb(b, next)
				if !ok {
					return
				}
				st.reg, st.offset, p = int(reg), int64(off), next2
			case 0x0d:
				reg, next, ok := uleb(b, p)
				if !ok {
					return
				}
				st.reg, p = int(reg), next
			case 0x0e:
				off, next, ok := uleb(b, p)
				if !ok {
					return
				}
				st.offset, p = int64(off), next
			case 0x11:
				if _, next, ok := uleb(b, p); ok {
					if _, next2, ok := uleb(b, next); ok {
						p = next2
						continue
					}
				}
				return
			case 0x12:
				reg, next, ok := uleb(b, p)
				if !ok {
					return
				}
				off, next2, ok := sleb(b, next)
				if !ok {
					return
				}
				st.reg, st.offset, p = int(reg), off, next2
			case 0x13:
				off, next, ok := sleb(b, p)
				if !ok {
					return
				}
				st.offset, p = off, next
			default:
				return
			}
		}
	}
	_ = table
}
