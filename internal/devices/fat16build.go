package devices

import (
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// fatNode is one host file or directory while an image is being built.
type fatNode struct {
	name          string
	root          bool
	dir           bool
	data          []byte
	mtime         time.Time
	children      []*fatNode
	short         [11]byte
	lfn           []uint16
	parentCluster int
	clusters      []int
}

const maxFAT16HostBytes int64 = 16 << 20

const (
	fatAttrReadOnly  = 0x01
	fatAttrHidden    = 0x02
	fatAttrSystem    = 0x04
	fatAttrVolume    = 0x08
	fatAttrDirectory = 0x10
	fatAttrArchive   = 0x20
)

// PrepareSDDir creates the backing directory and seeds it with a README when
// it is empty. It is safe to call on every startup.
func PrepareSDDir(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("sd dir: %w", err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("sd dir: %w", err)
	}
	if len(entries) == 0 {
		readme := "RP2350SIM SD card\r\n" +
			"Files in this directory are the card contents.\r\n" +
			"Changes made by the emulated firmware are written back here.\r\n"
		if err := os.WriteFile(filepath.Join(path, "README.TXT"), []byte(readme), 0o644); err != nil {
			return fmt.Errorf("sd dir: %w", err)
		}
	}
	return nil
}

// BuildFAT16FromDir packs a host directory into a FAT16 image and returns the
// image plus the relative paths present in it (the deletion manifest).
func BuildFAT16FromDir(root string, size int) ([]byte, map[string]bool, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	hostRoot, err := os.OpenRoot(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("sd dir: %w", err)
	}
	defer func() { _ = hostRoot.Close() }()
	info, err := hostRoot.Stat(".")
	if err != nil {
		return nil, nil, fmt.Errorf("sd dir: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("sd dir: %s is not a directory", abs)
	}
	if size < 8<<20 {
		size = 8 << 20
	}
	// 16 MiB cards are the useful default; keep the image a whole number of
	// sectors and let the layout reject sizes that are not FAT16.
	totalSectors := size / fatSectorSize
	l, err := buildLayout(totalSectors)
	if err != nil {
		return nil, nil, err
	}

	rootNode := &fatNode{name: "", root: true, dir: true}
	var hostBytes int64
	if err := scanHostDir(hostRoot, ".", rootNode, 0, &hostBytes); err != nil {
		return nil, nil, err
	}
	generateNames(rootNode)
	next := 2
	if err := allocate(rootNode, l, &next); err != nil {
		return nil, nil, err
	}
	if next > l.clusterCount()+2 {
		return nil, nil, fmt.Errorf("sd dir: directory does not fit in the image")
	}

	img := make([]byte, l.totalSectors*l.sectorSize)
	writeBootSector(img, l)
	l.setFATEntry(img, 0, 0xFFF8)
	l.setFATEntry(img, 1, 0xFFFF)
	for _, n := range allNodes(rootNode) {
		for i, c := range n.clusters {
			next := 0xFFFF
			if i+1 < len(n.clusters) {
				next = n.clusters[i+1]
			}
			l.setFATEntry(img, c, next)
		}
	}

	rootCapacity := l.rootEntries * 32
	rootBuf, err := encodeDir(rootNode, 0, true)
	if err != nil {
		return nil, nil, err
	}
	if len(rootBuf) > rootCapacity {
		return nil, nil, fmt.Errorf("sd dir: %d root entries exceed the %d-entry root directory", len(rootBuf)/32, l.rootEntries)
	}
	copy(img[l.rootOffset:l.rootOffset+rootCapacity], rootBuf)

	for _, n := range allNodes(rootNode) {
		if !n.dir || n == rootNode {
			continue
		}
		buf, err := encodeDir(n, n.parentCluster, false)
		if err != nil {
			return nil, nil, err
		}
		need := len(n.clusters) * l.clusterSize()
		if len(buf) > need {
			return nil, nil, fmt.Errorf("sd dir: directory %s outgrew its clusters", n.name)
		}
		writeClusters(img, l, n.clusters, buf)
	}
	for _, n := range allNodes(rootNode) {
		if n.dir {
			continue
		}
		writeClusters(img, l, n.clusters, n.data)
	}

	manifest := map[string]bool{}
	collectManifest(rootNode, "", manifest)
	return img, manifest, nil
}

func scanHostDir(root *os.Root, path string, node *fatNode, depth int, hostBytes *int64) error {
	if depth > 32 {
		return fmt.Errorf("sd dir: nesting deeper than 32 levels")
	}
	entries, err := fs.ReadDir(root.FS(), filepath.ToSlash(path))
	if err != nil {
		return fmt.Errorf("sd dir: %w", err)
	}
	for _, entry := range entries {
		rel := filepath.Join(path, entry.Name())
		info, err := root.Lstat(rel)
		if err != nil {
			return fmt.Errorf("sd dir: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue // do not follow links out of the card directory
		}
		child := &fatNode{name: entry.Name(), dir: info.IsDir(), mtime: info.ModTime()}
		if child.dir {
			if err := scanHostDir(root, rel, child, depth+1, hostBytes); err != nil {
				return err
			}
		} else if info.Mode().IsRegular() {
			remaining := maxFAT16HostBytes - *hostBytes
			if info.Size() < 0 || info.Size() > remaining {
				return fatHostBudgetError(rel)
			}
			child.data, err = readHostFile(root, rel, remaining)
			if err != nil {
				return err
			}
			*hostBytes += int64(len(child.data))
		} else {
			continue // sockets, devices and other special files
		}
		node.children = append(node.children, child)
	}
	return nil
}

func readHostFile(root *os.Root, rel string, remaining int64) ([]byte, error) {
	f, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("sd dir: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("sd dir: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > remaining {
		_ = f.Close()
		return nil, fatHostBudgetError(rel)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, remaining+1))
	closeErr := f.Close()
	if readErr != nil {
		return nil, fmt.Errorf("sd dir: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("sd dir: %w", closeErr)
	}
	if int64(len(data)) > remaining {
		return nil, fatHostBudgetError(rel)
	}
	return data, nil
}

func fatHostBudgetError(path string) error {
	return fmt.Errorf("sd dir: host file %q exceeds the 16 MiB cumulative budget", path)
}

// generateNames assigns a unique 8.3 name and an optional VFAT long name to
// every entry in a directory tree.
func generateNames(node *fatNode) {
	used := map[string]bool{}
	for _, child := range node.children {
		child.short = makeShortName(child.name, used)
		used[string(child.short[:])] = true
		if displayShort(child.short) != child.name {
			child.lfn = utf16.Encode([]rune(child.name))
		}
		if child.dir {
			generateNames(child)
		}
	}
}

func allocate(node *fatNode, l *fat16Layout, next *int) error {
	for _, child := range node.children {
		child.parentCluster = 0
		if !node.root && len(node.clusters) > 0 {
			child.parentCluster = node.clusters[0]
		}
		if child.dir {
			bytes := dirBytes(child)
			groups := (bytes + l.clusterSize() - 1) / l.clusterSize()
			if groups == 0 {
				groups = 1
			}
			child.clusters, *next = allocChain(*next, groups)
			if err := allocate(child, l, next); err != nil {
				return err
			}
		} else {
			groups := (len(child.data) + l.clusterSize() - 1) / l.clusterSize()
			child.clusters, *next = allocChain(*next, groups)
		}
	}
	return nil
}

func allocChain(next, n int) ([]int, int) {
	if n == 0 {
		return nil, next
	}
	out := make([]int, n)
	for i := range out {
		out[i] = next
		next++
	}
	return out, next
}

// dirBytes is the number of directory bytes a node needs, including the dot
// entries for subdirectories and VFAT long-name records.
func dirBytes(node *fatNode) int {
	n := len(node.children) * 32
	if !node.root {
		n += 64
	}
	for _, c := range node.children {
		if c.lfn != nil {
			n += ((len(c.lfn) + 12) / 13) * 32
		}
	}
	return n
}

func allNodes(root *fatNode) []*fatNode {
	var out []*fatNode
	var walk func(*fatNode)
	walk = func(n *fatNode) {
		out = append(out, n)
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(root)
	return out
}

func collectManifest(n *fatNode, rel string, out map[string]bool) {
	for _, c := range n.children {
		p := filepath.Join(rel, c.name)
		out[p] = true
		if c.dir {
			collectManifest(c, p, out)
		}
	}
}

func writeClusters(img []byte, l *fat16Layout, clusters []int, data []byte) {
	off := 0
	for _, c := range clusters {
		n := min(l.clusterSize(), len(data)-off)
		if n <= 0 {
			break
		}
		copy(img[l.clusterOffset(c):], data[off:off+n])
		off += n
	}
}

// encodeDir renders one directory (root or subdirectory) as FAT entries.
func encodeDir(node *fatNode, parent int, root bool) ([]byte, error) {
	var buf []byte
	add := func(b []byte) { buf = append(buf, b...) }

	if root {
		label := make([]byte, 32)
		copy(label[0:11], "RP2350SIM  ")
		label[11] = fatAttrVolume
		add(label)
	}
	if !root {
		dot := make([]byte, 32)
		copy(dot[0:11], ".          ")
		dot[11] = fatAttrDirectory
		if len(node.clusters) > 0 {
			binary.LittleEndian.PutUint16(dot[26:], uint16(node.clusters[0]))
		}
		add(dot)
		dd := make([]byte, 32)
		copy(dd[0:11], "..         ")
		dd[11] = fatAttrDirectory
		if parent >= 2 {
			binary.LittleEndian.PutUint16(dd[26:], uint16(parent))
		}
		add(dd)
	}

	for _, child := range node.children {
		if child.lfn != nil {
			add(lfnEntries(child.lfn, child.short))
		}
		e := make([]byte, 32)
		copy(e[0:11], child.short[:])
		attr := byte(fatAttrArchive)
		if child.dir {
			attr = fatAttrDirectory
		}
		e[11] = attr
		date, tm := dosTime(child.mtime)
		binary.LittleEndian.PutUint16(e[24:], date)
		binary.LittleEndian.PutUint16(e[22:], tm)
		if len(child.clusters) > 0 {
			binary.LittleEndian.PutUint16(e[26:], uint16(child.clusters[0]))
			binary.LittleEndian.PutUint16(e[20:], 0)
		}
		if !child.dir {
			binary.LittleEndian.PutUint32(e[28:], uint32(len(child.data)))
		}
		add(e)
	}
	return buf, nil
}

func lfnEntries(name []uint16, short [11]byte) []byte {
	n := (len(name) + 12) / 13
	if n == 0 {
		return nil
	}
	sum := lfnChecksum(short)
	buf := make([]byte, n*32)
	for seq := n; seq >= 1; seq-- {
		e := buf[(n-seq)*32:]
		ord := byte(seq)
		if seq == n {
			ord |= 0x40
		}
		e[0] = ord
		e[11] = 0x0F
		e[12] = 0
		e[13] = sum
		// The 13 UTF-16 units are split around the attr/type/checksum and
		// cluster fields: 5 + 6 + 2 code units.
		offsets := [...]int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30}
		for j, off := range offsets {
			idx := (seq-1)*13 + j
			c := uint16(0xFFFF)
			if idx < len(name) {
				c = name[idx]
			} else if idx == len(name) {
				c = 0
			}
			binary.LittleEndian.PutUint16(e[off:], c)
		}
	}
	return buf
}

func lfnChecksum(short [11]byte) byte {
	var sum byte
	for _, b := range short {
		sum = (sum >> 1) | (sum << 7)
		sum += b
	}
	return sum
}

func dosTime(t time.Time) (date, tm uint16) {
	if t.IsZero() {
		t = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	year := min(max(t.Year(), 1980), 2107)
	date = uint16((year-1980)<<9 | int(t.Month())<<5 | t.Day())
	tm = uint16(t.Hour()<<11 | t.Minute()<<5 | t.Second()/2)
	return
}

// makeShortName returns a unique 8.3 name for a host filename. Invalid
// characters become underscores; long or conflicting names get a ~N suffix.
func makeShortName(name string, used map[string]bool) [11]byte {
	stem, ext := name, ""
	if i := strings.LastIndex(name, "."); i > 0 {
		stem, ext = name[:i], name[i+1:]
	}
	clean := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToUpper(s) {
			if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
				strings.ContainsRune("$%'-_@~`!(){}^#&", r) {
				b.WriteRune(r)
			} else {
				b.WriteByte('_')
			}
		}
		return b.String()
	}
	stem = clean(stem)
	ext = clean(ext)
	if stem == "" {
		stem = "FILE"
	}
	if len(ext) > 3 {
		ext = ext[:3]
	}
	keyBytes := padNameBytes(stem, ext)
	if len(stem) <= 8 && !used[string(keyBytes[:])] && !reservedName(stem) {
		return keyBytes
	}
	base := stem
	for n := 1; n < 1000000; n++ {
		suffix := fmt.Sprintf("~%d", n)
		maxBase := 8 - len(suffix)
		if maxBase < 1 {
			break
		}
		b := base
		if len(b) > maxBase {
			b = b[:maxBase]
		}
		keyBytes := padNameBytes(b+suffix, ext)
		if !used[string(keyBytes[:])] {
			return keyBytes
		}
	}
	return padNameBytes("FILE", "TXT")
}

func padNameBytes(stem, ext string) [11]byte {
	var out [11]byte
	for i := range out {
		out[i] = ' '
	}
	copy(out[0:8], stem)
	if len(ext) > 3 {
		ext = ext[:3]
	}
	copy(out[8:11], ext)
	return out
}

func reservedName(stem string) bool {
	switch stem {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) &&
		stem[3] >= '1' && stem[3] <= '9' {
		return true
	}
	return false
}

func displayShort(short [11]byte) string {
	stem := strings.TrimRight(string(short[0:8]), " ")
	ext := strings.TrimRight(string(short[8:11]), " ")
	if ext == "" {
		return stem
	}
	return stem + "." + ext
}
