package debug

import (
	"encoding/binary"
	"fmt"
)

// localVar is a function argument or local from DWARF.
type localVar struct {
	Name   string
	Type   string
	Size   int64
	Loc    any
	Const  *uint64
	Ranges [][2]uint64
}

// VariablesAt evaluates the current function's locals and the global objects.
// DWARF expressions that cannot be evaluated are reported as unavailable; they
// are never guessed.
func (i *Info) VariablesAt(pc uint32, regs [16]uint32, read MemoryReader) []Variable {
	var out []Variable
	fn, ok := i.FunctionAt(pc)
	file := ""
	if ok {
		file = fn.File
	}
	var cfa uint64
	if ok {
		cfa = i.cfaAt(fn.Addr, pc, regs)
		fb := i.frameBaseFor(fn.Addr)
		for _, v := range i.locals[fn.Addr] {
			if len(v.Ranges) > 0 && !containsPC(v.Ranges, uint64(pc)) {
				continue
			}
			out = append(out, i.evalLocal(v, pc, regs, cfa, fb, read))
		}
	}
	for _, g := range i.globals {
		if g.Type == "" || g.Type == "object" {
			continue
		}
		if file != "" && g.File != "" && baseName(g.File) != baseName(file) {
			continue
		}
		if g.Const != nil {
			out = append(out, Variable{
				Name: g.Name, Type: g.Type, Scope: "global",
				Value: fmt.Sprintf("0x%x", *g.Const), Available: true,
			})
			continue
		}
		if g.Addr == 0 {
			continue
		}
		size := g.Size
		if size <= 0 || size > 8 {
			size = 4
		}
		raw, ok := read(g.Addr, int(size))
		if !ok {
			out = append(out, Variable{Name: g.Name, Type: g.Type, Scope: "global",
				Address: g.Addr, Note: "unreadable"})
			continue
		}
		out = append(out, Variable{
			Name: g.Name, Type: g.Type, Scope: "global", Address: g.Addr,
			Value: formatBytes(raw, int64(len(raw)), g.Type), Available: true,
		})
	}
	return out
}

func (i *Info) frameBaseFor(low uint32) []byte {
	return i.frameBase[low]
}

func (i *Info) evalLocal(v localVar, pc uint32, regs [16]uint32, cfa uint64, fb []byte, read MemoryReader) Variable {
	out := Variable{Name: v.Name, Type: v.Type, Scope: "local"}
	if v.Const != nil {
		out.Value = fmt.Sprintf("0x%x", *v.Const)
		out.Available = true
		return out
	}
	expr, ok := i.variableExpr(v.Loc, pc)
	if !ok {
		out.Note = "optimized out"
		return out
	}
	var frame *uint64
	if len(fb) > 0 {
		if base, at, ok := evalAddress(fb, &regs, &cfa); ok && at {
			frame = &base
		}
	}
	frameBase := cfa
	if frame != nil {
		frameBase = *frame
	}
	addr, isAddr, ok := evalAddressWithFrame(expr, &regs, frameBase, frame)
	if !ok {
		out.Note = "unsupported location"
		return out
	}
	if !isAddr {
		out.Value = fmt.Sprintf("0x%x", addr)
		out.Available = true
		return out
	}
	out.Address = uint32(addr)
	size := v.Size
	if size <= 0 {
		size = 4
	}
	if size > 8 {
		raw, ok := read(uint32(addr), 16)
		if !ok {
			out.Note = "unreadable"
			return out
		}
		out.Value = fmt.Sprintf("% x…", raw)
		out.Available = true
		return out
	}
	raw, ok := read(uint32(addr), int(size))
	if !ok {
		out.Note = "unreadable"
		return out
	}
	out.Value = formatBytes(raw, size, v.Type)
	out.Available = true
	return out
}

// variableExpr extracts an inline DWARF expression. Location lists are not
// decoded yet; their values are reported as unavailable rather than read from
// the wrong address.
func (i *Info) variableExpr(loc any, pc uint32) ([]byte, bool) {
	switch v := loc.(type) {
	case []byte:
		return v, true
	case int64:
		return i.locV4Expr(uint32(v), pc)
	default:
		return nil, false
	}
}

// locV4Expr walks a DWARF4 .debug_loc list. TinyGo emits these with absolute
// relocations, which readDebugLoc applies at load time.
func (i *Info) locV4Expr(off uint32, pc uint32) ([]byte, bool) {
	if i.locV4 == nil || int(off) >= len(i.locV4) {
		return nil, false
	}
	var base uint32
	for p := int(off); p+8 <= len(i.locV4); {
		begin := binary.LittleEndian.Uint32(i.locV4[p:])
		end := binary.LittleEndian.Uint32(i.locV4[p+4:])
		p += 8
		if begin == 0 && end == 0 {
			return nil, false
		}
		if begin == 0xffffffff {
			base = end
			continue
		}
		if base != 0 {
			begin += base
			end += base
		}
		if p+2 > len(i.locV4) {
			return nil, false
		}
		n := int(binary.LittleEndian.Uint16(i.locV4[p:]))
		p += 2
		if p+n > len(i.locV4) {
			return nil, false
		}
		if uint64(pc) >= uint64(begin) && uint64(pc) < uint64(end) {
			return i.locV4[p : p+n], true
		}
		p += n
	}
	return nil, false
}

const (
	opAddr       = 0x03
	opDeref      = 0x06
	opConst1u    = 0x08
	opConst1s    = 0x09
	opConst2u    = 0x0a
	opConst2s    = 0x0b
	opConst4u    = 0x0c
	opConst4s    = 0x0d
	opConst8u    = 0x0e
	opConst8s    = 0x0f
	opConstU     = 0x10
	opConstS     = 0x11
	opDup        = 0x12
	opDrop       = 0x13
	opOver       = 0x14
	opPick       = 0x15
	opSwap       = 0x16
	opRot        = 0x17
	opXDeref     = 0x18
	opAbs        = 0x19
	opAnd        = 0x1a
	opDiv        = 0x1b
	opMinus      = 0x1c
	opMod        = 0x1d
	opMul        = 0x1e
	opNeg        = 0x1f
	opNot        = 0x20
	opOr         = 0x21
	opPlus       = 0x22
	opPlusUconst = 0x23
	opShl        = 0x24
	opShr        = 0x25
	opShra       = 0x26
	opXor        = 0x27
	opSkip       = 0x2f
	opLit0       = 0x30
	opLit31      = 0x4f
	opReg0       = 0x50
	opReg31      = 0x6f
	opBreg0      = 0x70
	opBreg31     = 0x8f
	opRegX       = 0x90
	opFbreg      = 0x91
	opBregX      = 0x92
	opPiece      = 0x93
	opDerefSize  = 0x94
	opXderefSize = 0x95
	opNop        = 0x96
	opStackValue = 0x9f
)

type exprValue struct {
	u      uint64
	isAddr bool
	valid  bool
}

func evalAddress(expr []byte, regs *[16]uint32, cfa *uint64) (uint64, bool, bool) {
	return evalAddressWithFrame(expr, regs, deref(cfa), cfa)
}

func deref(p *uint64) uint64 {
	if p == nil {
		return 0
	}
	return *p
}

func evalAddressWithFrame(expr []byte, regs *[16]uint32, frame uint64, cfa *uint64) (uint64, bool, bool) {
	var stack []exprValue
	stackValue := false
	for pc := 0; pc < len(expr); {
		op := expr[pc]
		pc++
		push := func(v uint64, addr bool) { stack = append(stack, exprValue{u: v, isAddr: addr, valid: true}) }
		pop := func() (exprValue, bool) {
			if len(stack) == 0 {
				return exprValue{}, false
			}
			v := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			return v, true
		}
		switch {
		case op >= opLit0 && op <= opLit31:
			push(uint64(op-opLit0), false)
		case op >= opReg0 && op <= opReg31:
			reg := int(op - opReg0)
			if regs == nil || reg >= 16 {
				return 0, false, false
			}
			push(uint64((*regs)[reg]), false)
		case op >= opBreg0 && op <= opBreg31:
			reg := int(op - opBreg0)
			if regs == nil || reg >= 16 {
				return 0, false, false
			}
			n, next, ok := sleb(expr, pc)
			if !ok {
				return 0, false, false
			}
			pc = next
			push(uint64((*regs)[reg])+uint64(n), true)
		case op == opAddr:
			if pc+4 > len(expr) {
				return 0, false, false
			}
			push(uint64(binary.LittleEndian.Uint32(expr[pc:])), true)
			pc += 4
		case op == opFbreg:
			n, next, ok := sleb(expr, pc)
			if !ok {
				return 0, false, false
			}
			pc = next
			push(frame+uint64(n), true)
		case op == opBregX:
			reg, next, ok := uleb(expr, pc)
			if !ok {
				return 0, false, false
			}
			n, next2, ok := sleb(expr, next)
			if !ok || regs == nil || reg >= 16 {
				return 0, false, false
			}
			pc = next2
			push(uint64((*regs)[reg])+uint64(n), true)
		case op == opRegX:
			reg, next, ok := uleb(expr, pc)
			if !ok || regs == nil || reg >= 16 {
				return 0, false, false
			}
			pc = next
			push(uint64((*regs)[reg]), false)
		case op == opConst1u || op == opConst1s:
			if pc >= len(expr) {
				return 0, false, false
			}
			v := uint64(expr[pc])
			if op == opConst1s {
				v = uint64(int64(int8(expr[pc])))
			}
			pc++
			push(v, false)
		case op == opConst2u || op == opConst2s:
			if pc+2 > len(expr) {
				return 0, false, false
			}
			v := uint64(binary.LittleEndian.Uint16(expr[pc:]))
			if op == opConst2s {
				v = uint64(int64(int16(v)))
			}
			pc += 2
			push(v, false)
		case op == opConst4u || op == opConst4s:
			if pc+4 > len(expr) {
				return 0, false, false
			}
			v := uint64(binary.LittleEndian.Uint32(expr[pc:]))
			if op == opConst4s {
				v = uint64(int64(int32(v)))
			}
			pc += 4
			push(v, false)
		case op == opConst8u || op == opConst8s:
			if pc+8 > len(expr) {
				return 0, false, false
			}
			v := binary.LittleEndian.Uint64(expr[pc:])
			pc += 8
			push(v, false)
		case op == opConstU:
			v, next, ok := uleb(expr, pc)
			if !ok {
				return 0, false, false
			}
			pc = next
			push(v, false)
		case op == opConstS:
			v, next, ok := sleb(expr, pc)
			if !ok {
				return 0, false, false
			}
			pc = next
			push(uint64(v), false)
		case op == opPlusUconst:
			v, next, ok := uleb(expr, pc)
			if !ok {
				return 0, false, false
			}
			pc = next
			a, ok := pop()
			if !ok {
				return 0, false, false
			}
			push(a.u+v, a.isAddr)
		case op == opPlus || op == opMinus || op == opMul || op == opDiv || op == opMod:
			b, ok := pop()
			if !ok {
				return 0, false, false
			}
			a, ok := pop()
			if !ok {
				return 0, false, false
			}
			var v uint64
			switch op {
			case opPlus:
				v = a.u + b.u
			case opMinus:
				v = a.u - b.u
			case opMul:
				v = a.u * b.u
			case opDiv:
				if b.u == 0 {
					return 0, false, false
				}
				v = a.u / b.u
			case opMod:
				if b.u == 0 {
					return 0, false, false
				}
				v = a.u % b.u
			}
			push(v, a.isAddr)
		case op == opAnd || op == opOr || op == opXor:
			b, ok := pop()
			if !ok {
				return 0, false, false
			}
			a, ok := pop()
			if !ok {
				return 0, false, false
			}
			v := a.u
			switch op {
			case opAnd:
				v &= b.u
			case opOr:
				v |= b.u
			case opXor:
				v ^= b.u
			}
			push(v, false)
		case op == opNeg:
			a, ok := pop()
			if !ok {
				return 0, false, false
			}
			push(-a.u, false)
		case op == opNot:
			a, ok := pop()
			if !ok {
				return 0, false, false
			}
			push(^a.u, false)
		case op == opDup:
			a, ok := pop()
			if !ok {
				return 0, false, false
			}
			push(a.u, a.isAddr)
			push(a.u, a.isAddr)
		case op == opDrop:
			if _, ok := pop(); !ok {
				return 0, false, false
			}
		case op == opSwap:
			b, ok := pop()
			if !ok {
				return 0, false, false
			}
			a, ok := pop()
			if !ok {
				return 0, false, false
			}
			push(b.u, b.isAddr)
			push(a.u, a.isAddr)
		case op == opNop:
		case op == opStackValue:
			stackValue = true
		case op == opDeref:
			a, ok := pop()
			if !ok {
				return 0, false, false
			}
			// The caller has the memory reader; deref is not evaluated here.
			_ = a
			return 0, false, false
		case op == opSkip:
			if pc+2 > len(expr) {
				return 0, false, false
			}
			pc += 2 + int(int16(binary.LittleEndian.Uint16(expr[pc:])))
			if pc < 0 || pc > len(expr) {
				return 0, false, false
			}
		default:
			return 0, false, false
		}
	}
	if len(stack) == 0 {
		return 0, false, false
	}
	v := stack[len(stack)-1]
	if stackValue {
		return v.u, false, true
	}
	return v.u, v.isAddr, true
}

func uleb(b []byte, pc int) (uint64, int, bool) {
	var v uint64
	var shift uint
	for pc < len(b) {
		c := b[pc]
		pc++
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return v, pc, true
		}
		shift += 7
		if shift >= 64 {
			return 0, pc, false
		}
	}
	return 0, pc, false
}

func sleb(b []byte, pc int) (int64, int, bool) {
	u, next, ok := uleb(b, pc)
	if !ok {
		return 0, next, false
	}
	shift := uint(next-pc) * 7
	if shift < 64 && u&(1<<(shift-1)) != 0 {
		u |= ^uint64(0) << shift
	}
	return int64(u), next, true
}

// cfaAt returns the CFA for a function at pc. It uses a small .debug_frame
// reader; when no rule exists the current SP is the fallback.
func (i *Info) cfaAt(low, pc uint32, regs [16]uint32) uint64 {
	if i.cfa != nil {
		if cfa, ok := i.cfa.at(low, pc, regs); ok {
			return cfa
		}
	}
	return uint64(regs[13])
}

func baseName(p string) string {
	for n := len(p) - 1; n >= 0; n-- {
		if p[n] == '/' || p[n] == '\\' {
			return p[n+1:]
		}
	}
	return p
}
