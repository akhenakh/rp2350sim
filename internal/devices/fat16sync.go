package devices

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

// fatDirEntry is one decoded directory record.
type fatDirEntry struct {
	name    string
	attr    byte
	cluster int
	size    int
	mtime   time.Time
}

// Sync mirrors the card image back to its backing directory. It is called
// after writes settle; the caller serialises it with the emulated CPU.
func (s *SDCard) Sync() error {
	if s.Dir == "" {
		return nil
	}
	l, err := parseFAT16(s.image)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return fmt.Errorf("sd sync: %w", err)
	}
	defer root.Close()

	manifest := map[string]bool{}
	if err := s.syncLevel(root, "", 0, true, l, manifest); err != nil {
		return err
	}
	if err := s.removeStale(root, manifest); err != nil {
		return err
	}
	s.manifest = manifest
	s.dirty = false
	return nil
}

func (s *SDCard) syncLevel(root *os.Root, rel string, cluster int, rootLevel bool, l *fat16Layout, manifest map[string]bool) error {
	if err := ensureHostDir(root, rel); err != nil {
		return err
	}
	entries, err := readDirEntries(s.image, l, cluster, rootLevel)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.attr&fatAttrVolume != 0 {
			continue
		}
		name := safeHostName(e.name)
		if name == "" || name == "." || name == ".." {
			continue
		}
		childRel := filepath.Join(rel, name)
		if e.attr&fatAttrDirectory != 0 {
			manifest[childRel] = true
			if err := s.syncLevel(root, childRel, e.cluster, false, l, manifest); err != nil {
				return err
			}
			continue
		}
		data, err := readClusterChain(s.image, l, e.cluster, e.size)
		if err != nil {
			return fmt.Errorf("sd sync: %s: %w", childRel, err)
		}
		if err := s.writeHostFile(root, childRel, data); err != nil {
			return err
		}
		manifest[childRel] = true
	}
	return nil
}

// removeStale deletes files that disappeared from the card, but never touches
// paths that were not present at the previous sync. Directories are removed
// only when empty, so files the user added in the meantime survive.
func (s *SDCard) removeStale(root *os.Root, manifest map[string]bool) error {
	type stale struct {
		rel   string
		depth int
	}
	var gone []stale
	for rel := range s.manifest {
		if !manifest[rel] {
			gone = append(gone, stale{rel: rel, depth: strings.Count(rel, string(filepath.Separator))})
		}
	}
	sort.Slice(gone, func(i, j int) bool {
		if gone[i].depth != gone[j].depth {
			return gone[i].depth > gone[j].depth
		}
		return gone[i].rel > gone[j].rel
	})
	for _, g := range gone {
		if !filepath.IsLocal(g.rel) || g.rel == "." {
			continue
		}
		if _, err := root.Lstat(g.rel); err != nil {
			continue
		}
		_ = root.Remove(g.rel) // directories are removed only when empty
	}
	return nil
}

func (s *SDCard) writeHostFile(root *os.Root, rel string, data []byte) error {
	if !filepath.IsLocal(rel) || rel == "." {
		return fmt.Errorf("sd sync: unsafe path %q", rel)
	}
	dir := filepath.Dir(rel)
	if err := ensureHostDir(root, dir); err != nil {
		return err
	}
	tmpRel := filepath.Join(dir, ".rp2350sim-"+rand.Text())
	tmp, err := root.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("sd sync: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = root.Remove(tmpRel)
		return fmt.Errorf("sd sync: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		_ = root.Remove(tmpRel)
		return fmt.Errorf("sd sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = root.Remove(tmpRel)
		return fmt.Errorf("sd sync: %w", err)
	}
	if err := root.Rename(tmpRel, rel); err != nil {
		_ = root.Remove(tmpRel)
		return fmt.Errorf("sd sync: %w", err)
	}
	return nil
}

// ensureHostDir creates rel one component at a time and rejects symlinks,
// while os.Root prevents a concurrent component replacement from escaping.
func ensureHostDir(root *os.Root, rel string) error {
	clean := filepath.Clean(rel)
	if clean == "." {
		return nil
	}
	if !filepath.IsLocal(clean) {
		return fmt.Errorf("sd sync: unsafe path %q", rel)
	}
	cur := ""
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := root.Lstat(cur)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("sd sync: %s is a symlink", cur)
			}
			if !info.IsDir() {
				return fmt.Errorf("sd sync: %s is not a directory", cur)
			}
			continue
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("sd sync: %w", err)
		}
		if err := root.Mkdir(cur, 0o755); err != nil {
			return fmt.Errorf("sd sync: %w", err)
		}
	}
	return nil
}

// safeHostName strips path separators and NUL from a FAT name.
func safeHostName(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "\x00", "")
	name = strings.TrimRight(name, " .")
	return name
}

// readDirEntries decodes a root or cluster-backed directory.
func readDirEntries(img []byte, l *fat16Layout, cluster int, root bool) ([]fatDirEntry, error) {
	var data []byte
	if root {
		off := l.rootOffset
		data = img[off : off+l.rootEntries*32]
	} else {
		var err error
		data, err = chainBytes(img, l, cluster)
		if err != nil {
			return nil, err
		}
	}

	var out []fatDirEntry
	var lfn []uint16
	var lfnSum byte
	for off := 0; off+32 <= len(data); off += 32 {
		e := data[off : off+32]
		if e[0] == 0x00 {
			break
		}
		if e[0] == 0xE5 {
			lfn = nil
			continue
		}
		attr := e[11]
		if attr&0x3F == 0x0F {
			ord := e[0]
			if ord&0x40 != 0 {
				n := int(ord & 0x1F)
				if n == 0 || n > 20 {
					continue
				}
				lfn = make([]uint16, n*13)
				lfnSum = e[13]
			}
			if lfn == nil {
				continue
			}
			seq := int(ord & 0x1F)
			if seq < 1 || seq > len(lfn)/13 {
				continue
			}
			base := (seq - 1) * 13
			offsets := [...]int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30}
			for j, off := range offsets {
				lfn[base+j] = binary.LittleEndian.Uint16(e[off:])
			}
			continue
		}
		if attr&0x08 != 0 { // volume label
			lfn = nil
			continue
		}
		var short [11]byte
		copy(short[:], e[0:11])
		name := displayShort(short)
		if name == "." || name == ".." {
			lfn = nil
			continue
		}
		if lfn != nil && lfnChecksum(short) == lfnSum {
			name = decodeLFN(lfn)
		}
		lfn = nil
		out = append(out, fatDirEntry{
			name:    name,
			attr:    attr,
			cluster: int(binary.LittleEndian.Uint16(e[26:])),
			size:    int(binary.LittleEndian.Uint32(e[28:])),
			mtime:   fatTime(binary.LittleEndian.Uint16(e[24:]), binary.LittleEndian.Uint16(e[22:])),
		})
	}
	return out, nil
}

func decodeLFN(units []uint16) string {
	end := len(units)
	for i, u := range units {
		if u == 0 {
			end = i
			break
		}
	}
	units = units[:end]
	return string(utf16.Decode(units))
}

func fatTime(date, tm uint16) time.Time {
	if date == 0 {
		return time.Time{}
	}
	year := int(date>>9) + 1980
	month := time.Month((date >> 5) & 0xF)
	day := int(date & 0x1F)
	hour := int(tm >> 11)
	minute := int((tm >> 5) & 0x3F)
	sec := int(tm&0x1F) * 2
	return time.Date(year, month, day, hour, minute, sec, 0, time.Local)
}

// chainBytes returns all clusters of a directory chain.
func chainBytes(img []byte, l *fat16Layout, start int) ([]byte, error) {
	var out []byte
	seen := map[int]bool{}
	for c := start; c >= 2 && c < 0xFFF8; {
		if seen[c] {
			return nil, fmt.Errorf("fat: cluster chain loop at %d", c)
		}
		seen[c] = true
		off := l.clusterOffset(c)
		if off < 0 || off+l.clusterSize() > len(img) {
			return nil, fmt.Errorf("fat: cluster %d outside image", c)
		}
		out = append(out, img[off:off+l.clusterSize()]...)
		n, err := l.fatEntry(img, c)
		if err != nil {
			return nil, err
		}
		c = n
	}
	return out, nil
}

// readClusterChain reads at most size bytes (all bytes when size < 0).
func readClusterChain(img []byte, l *fat16Layout, start, size int) ([]byte, error) {
	if start < 2 {
		if size > 0 {
			return nil, fmt.Errorf("fat: empty chain for %d-byte file", size)
		}
		return nil, nil
	}
	var out []byte
	seen := map[int]bool{}
	for c := start; c >= 2 && c < 0xFFF8; {
		if seen[c] {
			return nil, fmt.Errorf("fat: cluster chain loop at %d", c)
		}
		seen[c] = true
		off := l.clusterOffset(c)
		if off < 0 || off+l.clusterSize() > len(img) {
			return nil, fmt.Errorf("fat: cluster %d outside image", c)
		}
		take := l.clusterSize()
		if size >= 0 && len(out)+take > size {
			take = size - len(out)
		}
		out = append(out, img[off:off+take]...)
		if size >= 0 && len(out) >= size {
			break
		}
		n, err := l.fatEntry(img, c)
		if err != nil {
			return nil, err
		}
		c = n
	}
	if size >= 0 && len(out) < size {
		return nil, fmt.Errorf("fat: chain ended at %d of %d bytes", len(out), size)
	}
	return out, nil
}
