// Package debug exposes the symbols, source lines and variables of an ELF
// firmware image to the emulator's debugger.
//
// Breakpoints and memory inspection only need addresses. This package adds the
// symbol table and DWARF information that make those addresses readable.
// It deliberately evaluates only the DWARF location forms a Cortex-M compiler
// commonly emits: registers, stack-relative frame slots, absolute addresses and
// simple constants. Locations that need an unsupported expression are reported
// as unavailable instead of showing a wrong value.
package debug

import (
	"debug/dwarf"
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Function is one code symbol.
type Function struct {
	Name    string
	Linkage string
	File    string
	Line    int
	Addr    uint32
	Size    uint32
}

// Global is one global or static object.
type Global struct {
	Name  string
	Type  string
	File  string
	Addr  uint32
	Size  int64
	Const *uint64
}

// Variable is a value presented by the debugger.
type Variable struct {
	Name      string
	Type      string
	Scope     string // "local" or "global"
	Value     string
	Address   uint32
	Available bool
	Note      string
}

// MemoryReader reads size bytes at addr. It must be side-effect free.
type MemoryReader func(addr uint32, size int) ([]byte, bool)

// Info is one ELF image's debug view.
type Info struct {
	Path      string
	Functions []Function
	Globals   []Global

	data      *dwarf.Data
	lines     []lineUnit
	globals   []Global
	locals    map[uint32][]localVar // function low PC -> variables
	frameBase map[uint32][]byte     // function low PC -> DW_AT_frame_base
	cfa       *cfaTable
	locV4     []byte
}

type lineUnit struct {
	reader  *dwarf.LineReader
	low     uint64
	high    uint64
	compDir string
}

// Load parses an ELF image. A missing or malformed DWARF section is not an
// error: the symbol table still supports breakpoints and function names.
func Load(path string) (*Info, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	info := &Info{
		Path:      path,
		locals:    map[uint32][]localVar{},
		frameBase: map[uint32][]byte{},
	}
	info.readSymbols(f)

	if data, derr := f.DWARF(); derr == nil {
		info.data = data
		info.readDWARF(data)
	}
	info.locV4 = readDebugLoc(f)
	info.cfa = parseCFA(f)
	info.sortFunctions()
	return info, nil
}

func (i *Info) readSymbols(f *elf.File) {
	syms, err := f.Symbols()
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, s := range syms {
		switch elf.ST_TYPE(s.Info) {
		case elf.STT_FUNC:
			if s.Value == 0 {
				continue
			}
			name := s.Name
			if name == "" || seen["f:"+name] {
				continue
			}
			seen["f:"+name] = true
			i.Functions = append(i.Functions, Function{
				Name: name, Linkage: name, Addr: uint32(s.Value &^ 1), Size: uint32(s.Size),
			})
		case elf.STT_OBJECT:
			if s.Value == 0 || s.Name == "" {
				continue
			}
			i.Globals = append(i.Globals, Global{
				Name: s.Name, Addr: uint32(s.Value), Size: int64(s.Size), Type: "object",
			})
		}
	}
}

func (i *Info) readDWARF(data *dwarf.Data) {
	r := data.Reader()
	for {
		e, err := r.Next()
		if err != nil || e == nil {
			return
		}
		if e.Tag != dwarf.TagCompileUnit {
			continue
		}
		compDir, _ := e.Val(dwarf.AttrCompDir).(string)
		var lr *dwarf.LineReader
		if parsed, lerr := data.LineReader(e); lerr == nil {
			lr = parsed
			low, high := i.unitRange(e)
			i.lines = append(i.lines, lineUnit{reader: lr, low: low, high: high, compDir: compDir})
		}
		i.readUnit(r, e, compDir, lr)
	}
}

func (i *Info) unitRange(e *dwarf.Entry) (uint64, uint64) {
	ranges, err := i.data.Ranges(e)
	if err != nil || len(ranges) == 0 {
		return 0, ^uint64(0)
	}
	low, high := ranges[0][0], ranges[0][1]
	for _, r := range ranges[1:] {
		low = min(low, r[0])
		high = max(high, r[1])
	}
	return low, high
}

func (i *Info) readUnit(r *dwarf.Reader, cu *dwarf.Entry, compDir string, lr *dwarf.LineReader) {
	cuRanges, _ := i.data.Ranges(cu)
	for {
		e, err := r.Next()
		if err != nil || e == nil {
			return
		}
		if e.Tag == 0 {
			continue
		}
		if e.Tag == dwarf.TagCompileUnit {
			r.Seek(e.Offset)
			return
		}
		if e.Tag == dwarf.TagSubprogram {
			i.readSubprogram(r, e, compDir, lr, cuRanges)
			continue
		}
		if e.Tag == dwarf.TagVariable {
			i.readGlobal(e, compDir, lr)
		}
		if e.Children {
			r.SkipChildren()
		}
	}
}

func (i *Info) readSubprogram(r *dwarf.Reader, e *dwarf.Entry, compDir string, lr *dwarf.LineReader, cuRanges [][2]uint64) {
	if decl, _ := e.Val(dwarf.AttrDeclaration).(bool); decl {
		if e.Children {
			r.SkipChildren()
		}
		return
	}
	ranges, _ := i.data.Ranges(e)
	low, high := firstRange(ranges)
	if low == 0 {
		low, high = firstRange(cuRanges)
	}
	if low == 0 {
		if e.Children {
			r.SkipChildren()
		}
		return
	}
	name := dlName(e)
	fn := Function{Name: name, Addr: uint32(low &^ 1), Size: uint32(high - low)}
	if lname, _ := e.Val(dwarf.AttrLinkageName).(string); lname != "" {
		fn.Linkage = lname
	} else {
		fn.Linkage = name
	}
	fn.Line = int(constInt(e.Val(dwarf.AttrDeclLine)))
	if file, line := i.declFileLine(e, compDir, lr); file != "" {
		fn.File, fn.Line = file, line
	}
	i.mergeFunction(fn)
	if fb, ok := e.Val(dwarf.AttrFrameBase).([]byte); ok {
		i.frameBase[uint32(low&^1)] = fb
	}

	var vars []localVar
	i.readVars(r, e, ranges, &vars)
	key := uint32(low &^ 1)
	if len(vars) > 0 || len(i.locals[key]) == 0 {
		i.locals[key] = vars
	}
}

// readVars consumes the children of a subprogram or lexical block. Nested
// blocks narrow the PC range of their variables.
const tagLexicalBlock = dwarf.Tag(0x0b)

func (i *Info) readVars(r *dwarf.Reader, parent *dwarf.Entry, ranges [][2]uint64, out *[]localVar) {
	if !parent.Children {
		return
	}
	for {
		e, err := r.Next()
		if err != nil || e == nil || e.Tag == 0 {
			return
		}
		switch e.Tag {
		case dwarf.TagVariable, dwarf.TagFormalParameter:
			if v, ok := i.localFromEntry(e, ranges); ok {
				*out = append(*out, v)
			}
			if e.Children {
				r.SkipChildren()
			}
		case tagLexicalBlock:
			blockRanges := ranges
			if rs, err := i.data.Ranges(e); err == nil && len(rs) > 0 {
				blockRanges = rs
			}
			i.readVars(r, e, blockRanges, out)
		default:
			if e.Children {
				r.SkipChildren()
			}
		}
	}
}

func (i *Info) localFromEntry(e *dwarf.Entry, ranges [][2]uint64) (localVar, bool) {
	name := dlName(e)
	if name == "" {
		return localVar{}, false
	}
	v := localVar{Name: name}
	v.Type, v.Size = i.typeOf(e)
	v.Loc = e.Val(dwarf.AttrLocation)
	v.Const, _ = i.uintConst(e)
	if rs, err := i.data.Ranges(e); err == nil && len(rs) > 0 {
		v.Ranges = rs
	} else {
		v.Ranges = ranges
	}
	return v, true
}

func (i *Info) readGlobal(e *dwarf.Entry, compDir string, lr *dwarf.LineReader) {
	name := dlName(e)
	if name == "" {
		return
	}
	g := Global{Name: name}
	g.Type, g.Size = i.typeOf(e)
	g.File, _ = i.declFileLine(e, compDir, lr)
	g.Const, _ = i.uintConst(e)
	if loc, ok := e.Val(dwarf.AttrLocation).([]byte); ok {
		if addr, at, ok := evalAddress(loc, nil, nil); ok && at {
			g.Addr = uint32(addr)
		}
	}
	if g.Addr == 0 && g.Const == nil {
		// The symbol table usually has the address even when the location is
		// a location list or a register expression.
		for _, s := range i.Globals {
			if s.Name == name {
				g.Addr, g.Size = s.Addr, s.Size
				break
			}
		}
	}
	if g.Addr == 0 && g.Const == nil {
		return
	}
	i.globals = append(i.globals, g)
}

func (i *Info) mergeFunction(fn Function) {
	for n, old := range i.Functions {
		if old.Addr == fn.Addr || (old.Linkage != "" && old.Linkage == fn.Linkage) {
			if old.File == "" {
				i.Functions[n].File = fn.File
				i.Functions[n].Line = fn.Line
			}
			return
		}
	}
	i.Functions = append(i.Functions, fn)
}

func (i *Info) sortFunctions() {
	sort.Slice(i.Functions, func(a, b int) bool {
		if i.Functions[a].Addr == i.Functions[b].Addr {
			return i.Functions[a].Name < i.Functions[b].Name
		}
		return i.Functions[a].Addr < i.Functions[b].Addr
	})
	sort.Slice(i.Globals, func(a, b int) bool { return i.Globals[a].Name < i.Globals[b].Name })
	// Keep DWARF-typed globals and use the symbol table as a fallback.
	for _, sym := range i.Globals {
		seen := false
		for _, g := range i.globals {
			if g.Name == sym.Name || (g.Addr != 0 && g.Addr == sym.Addr) {
				seen = true
				break
			}
		}
		if !seen {
			i.globals = append(i.globals, sym)
		}
	}
	sort.Slice(i.globals, func(a, b int) bool { return i.globals[a].Name < i.globals[b].Name })
}

func dlName(e *dwarf.Entry) string {
	if n, _ := e.Val(dwarf.AttrName).(string); n != "" {
		return n
	}
	if n, _ := e.Val(dwarf.AttrLinkageName).(string); n != "" {
		return n
	}
	return ""
}

func (i *Info) typeOf(e *dwarf.Entry) (string, int64) {
	off, ok := e.Val(dwarf.AttrType).(dwarf.Offset)
	if !ok || i.data == nil {
		return "", 0
	}
	t, err := i.data.Type(off)
	if err != nil || t == nil {
		return "", 0
	}
	return t.String(), t.Size()
}

func (i *Info) uintConst(e *dwarf.Entry) (*uint64, bool) {
	v := e.Val(dwarf.AttrConstValue)
	switch n := v.(type) {
	case int64:
		u := uint64(n)
		return &u, true
	case uint64:
		return &n, true
	}
	return nil, false
}

func (i *Info) declFileLine(e *dwarf.Entry, compDir string, lr *dwarf.LineReader) (string, int) {
	line, _ := e.Val(dwarf.AttrDeclLine).(int64)
	idx, _ := e.Val(dwarf.AttrDeclFile).(int64)
	if idx <= 0 {
		return "", int(line)
	}
	if lr != nil {
		files := lr.Files()
		if int(idx) <= len(files) && files[idx-1] != nil {
			name := files[idx-1].Name
			if !filepath.IsAbs(name) && compDir != "" {
				name = filepath.Join(compDir, name)
			}
			return name, int(line)
		}
	}
	return "", int(line)
}

// Resolve looks up a function or global by name. Exact names win; a short
// name such as "main" also matches "main.main" when no exact symbol exists.
func (i *Info) Resolve(name string) (uint32, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, false
	}
	for _, f := range i.Functions {
		if f.Name == name || f.Linkage == name {
			return f.Addr, true
		}
	}
	for _, g := range i.Globals {
		if g.Name == name {
			return g.Addr, g.Addr != 0
		}
	}
	for _, f := range i.Functions {
		if strings.HasSuffix(f.Name, "."+name) || strings.HasSuffix(f.Linkage, "."+name) {
			return f.Addr, true
		}
	}
	for _, g := range i.Globals {
		if strings.HasSuffix(g.Name, "."+name) {
			return g.Addr, g.Addr != 0
		}
	}
	for _, f := range i.Functions {
		if strings.EqualFold(f.Name, name) || strings.EqualFold(f.Linkage, name) {
			return f.Addr, true
		}
	}
	return 0, false
}

// FunctionAt returns the function containing pc.
func (i *Info) FunctionAt(pc uint32) (Function, bool) {
	for _, f := range slices.Backward(i.Functions) {
		if pc < f.Addr {
			continue
		}
		if f.Size == 0 || pc < f.Addr+f.Size {
			return f, true
		}
	}
	return Function{}, false
}

// LineAt maps pc to the closest line-table entry at or before it. Multiple
// compilations can overlap in address, so the highest matching address wins.
func (i *Info) LineAt(pc uint32) (string, int, bool) {
	var bestAddr uint64
	var bestFile string
	var bestCompDir string
	var bestLine int
	found := false
	for _, l := range i.lines {
		if l.reader == nil {
			continue
		}
		l.reader.Reset()
		var e dwarf.LineEntry
		for l.reader.Next(&e) == nil {
			if e.Address > uint64(pc) {
				break
			}
			if e.EndSequence || e.File == nil {
				continue
			}
			if !found || e.Address >= bestAddr {
				bestAddr = e.Address
				bestFile = e.File.Name
				bestCompDir = l.compDir
				bestLine = e.Line
				found = true
			}
		}
	}
	if found {
		name := bestFile
		if bestCompDir != "" && !filepath.IsAbs(name) {
			name = filepath.Join(bestCompDir, name)
		}
		line := bestLine
		if line == 0 {
			if fn, ok := i.FunctionAt(pc); ok {
				line = fn.Line
			}
		}
		return name, line, true
	}
	if fn, ok := i.FunctionAt(pc); ok && fn.File != "" {
		return fn.File, fn.Line, true
	}
	return "", 0, false
}

// Source returns the source lines around line in file. It is a convenience for
// the UI; missing source files are not an error.
func Source(file string, line, context int) ([]string, int, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, 0, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if line < 1 {
		line = 1
	}
	start := max(1, line-context)
	end := min(len(lines), line+context)
	out := make([]string, 0, end-start+1)
	for n := start; n <= end; n++ {
		out = append(out, lines[n-1])
	}
	return out, start, nil
}

// Globals returns the object symbols, with DWARF type information when it is
// available.
func (i *Info) GlobalsList() []Global {
	out := append([]Global(nil), i.globals...)
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// LookupGlobal returns a global by exact or suffix name.
func (i *Info) LookupGlobal(name string) (Global, bool) {
	for _, g := range i.globals {
		if g.Name == name {
			return g, true
		}
	}
	for _, g := range i.globals {
		if strings.HasSuffix(g.Name, "."+name) {
			return g, true
		}
	}
	return Global{}, false
}

func firstRange(ranges [][2]uint64) (uint64, uint64) {
	if len(ranges) == 0 {
		return 0, 0
	}
	low, high := ranges[0][0], ranges[0][1]
	return low, high
}

func containsPC(ranges [][2]uint64, pc uint64) bool {
	for _, r := range ranges {
		if pc >= r[0] && pc < r[1] {
			return true
		}
	}
	return false
}

func formatBytes(raw []byte, size int64, typ string) string {
	if size <= 0 {
		size = int64(len(raw))
	}
	if size > 8 {
		n := min(len(raw), 16)
		return fmt.Sprintf("% x…", raw[:n])
	}
	var v uint64
	for _, b := range slices.Backward(raw) {
		v = v<<8 | uint64(b)
	}
	return fmt.Sprintf("0x%0*x", len(raw)*2, v)
}

func constInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case uint64:
		return int64(n)
	default:
		return 0
	}
}
