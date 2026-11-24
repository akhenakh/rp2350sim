package devices

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

func writeTestFile(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func dirSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestBuildFAT16FromDir packs long names, empty files and nested directories
// and checks the volume with fsck.fat when it is available.
func TestBuildFAT16FromDir(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, src, "hello world.txt", []byte("hello from the host\n"))
	writeTestFile(t, src, "EMPTY.TXT", nil)
	writeTestFile(t, src, "sub/notes.md", []byte("# notes\n"))
	writeTestFile(t, src, "sub/deeper/data.bin", []byte{0, 1, 2, 3, 0xFE, 0xFF})

	img, manifest, err := BuildFAT16FromDir(src, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	l, err := parseFAT16(img)
	if err != nil {
		t.Fatal(err)
	}
	root, err := readDirEntries(img, l, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]fatDirEntry{}
	for _, e := range root {
		byName[e.name] = e
	}
	hello, ok := byName["hello world.txt"]
	if !ok {
		t.Fatalf("long name missing from root: %v", byName)
	}
	data, err := readClusterChain(img, l, hello.cluster, hello.size)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello from the host\n" {
		t.Fatalf("hello = %q", data)
	}
	sub, ok := byName["sub"]
	if !ok || sub.attr&fatAttrDirectory == 0 {
		t.Fatalf("subdirectory missing: %v", byName)
	}
	subEntries, err := readDirEntries(img, l, sub.cluster, false)
	if err != nil {
		t.Fatal(err)
	}
	subNames := map[string]fatDirEntry{}
	for _, e := range subEntries {
		subNames[e.name] = e
	}
	if _, ok := subNames["notes.md"]; !ok {
		t.Fatalf("notes.md missing from sub: %+v", subEntries)
	}
	if _, ok := subNames["deeper"]; !ok {
		t.Fatalf("deeper missing from sub: %+v", subEntries)
	}
	for _, p := range []string{"hello world.txt", "EMPTY.TXT", "sub", "sub/notes.md", "sub/deeper", "sub/deeper/data.bin"} {
		if !manifest[p] {
			t.Errorf("manifest lacks %q: %v", p, manifest)
		}
	}

	if _, err := exec.LookPath("fsck.fat"); err == nil {
		path := filepath.Join(t.TempDir(), "card.img")
		if err := os.WriteFile(path, img, 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("fsck.fat", "-n", path).CombinedOutput()
		if err != nil {
			t.Fatalf("fsck.fat: %v\n%s", err, out)
		}
	}
}

// TestSyncFAT16ToDir mirrors an image into a host directory and removes files
// that disappeared, while leaving unrelated host files alone.
func TestSyncFAT16ToDir(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, src, "a.txt", []byte("A"))
	writeTestFile(t, src, "sub/b.txt", []byte("B"))
	imgA, manifestA, err := BuildFAT16FromDir(src, 16<<20)
	if err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	writeTestFile(t, dst, "keep.me", []byte("host file"))
	card, err := NewSDCard(rp2350.New(), -1, "", dst)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the card contents with image A, keeping dst's manifest as the
	// previous state so stale README.TXT is removed.
	card.image = imgA
	card.manifest = map[string]bool{"README.TXT": true}
	if err := card.Sync(); err != nil {
		t.Fatal(err)
	}
	got := dirSnapshot(t, dst)
	if got["a.txt"] != "A" || got["sub/b.txt"] != "B" {
		t.Fatalf("sync did not write files: %v", got)
	}
	if _, ok := got["README.TXT"]; ok {
		t.Fatal("stale README.TXT was not removed")
	}
	if got["keep.me"] != "host file" {
		t.Fatal("unrelated host file was removed")
	}

	// A new image without a.txt must delete it, but not keep.me.
	src2 := t.TempDir()
	writeTestFile(t, src2, "c.txt", []byte("C"))
	imgC, manifestC, err := BuildFAT16FromDir(src2, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	card.image = imgC
	card.manifest = manifestA
	if err := card.Sync(); err != nil {
		t.Fatal(err)
	}
	got = dirSnapshot(t, dst)
	if _, ok := got["a.txt"]; ok {
		t.Fatal("deleted a.txt stayed on the host")
	}
	if got["c.txt"] != "C" {
		t.Fatalf("c.txt missing: %v", got)
	}
	if got["keep.me"] != "host file" {
		t.Fatal("unrelated host file was removed")
	}
	_ = manifestC
}

func TestSyncFAT16RejectsEscapingSymlinkParent(t *testing.T) {
	t.Run("stale deletion", func(t *testing.T) {
		src := t.TempDir()
		img, _, err := BuildFAT16FromDir(src, 16<<20)
		if err != nil {
			t.Fatal(err)
		}
		dst := t.TempDir()
		external := t.TempDir()
		writeTestFile(t, external, "victim.txt", []byte("host data"))
		if err := os.Mkdir(filepath.Join(dst, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(dst, "sub")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(dst, "sub")); err != nil {
			t.Fatal(err)
		}

		card, err := NewSDCard(rp2350.New(), -1, "", dst)
		if err != nil {
			t.Fatal(err)
		}
		card.image = img
		card.manifest = map[string]bool{"sub": true, "sub/victim.txt": true}
		if err := card.Sync(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(external, "victim.txt"))
		if err != nil {
			t.Fatalf("external target was removed: %v", err)
		}
		if string(got) != "host data" {
			t.Fatalf("external target = %q", got)
		}
	})

	t.Run("directory creation and file write", func(t *testing.T) {
		src := t.TempDir()
		writeTestFile(t, src, "sub/new.txt", []byte("card data"))
		img, _, err := BuildFAT16FromDir(src, 16<<20)
		if err != nil {
			t.Fatal(err)
		}
		dst := t.TempDir()
		external := t.TempDir()
		if err := os.Symlink(external, filepath.Join(dst, "sub")); err != nil {
			t.Fatal(err)
		}

		card, err := NewSDCard(rp2350.New(), -1, "", dst)
		if err != nil {
			t.Fatal(err)
		}
		card.image = img
		if err := card.Sync(); err == nil {
			t.Fatal("sync followed a symlink outside the backing directory")
		}
		if _, err := os.Stat(filepath.Join(external, "new.txt")); !os.IsNotExist(err) {
			t.Fatalf("external file was created: %v", err)
		}
	})
}

func TestBuildFAT16FromDirHostBudget(t *testing.T) {
	tests := []struct {
		name  string
		sizes []int64
	}{
		{name: "sparse file", sizes: []int64{(16 << 20) + 1}},
		{name: "cumulative overflow", sizes: []int64{9 << 20, 9 << 20}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := t.TempDir()
			for i, size := range tt.sizes {
				name := filepath.Join(src, fmt.Sprintf("sparse-%d.bin", i))
				f, err := os.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.Truncate(size); err != nil {
					_ = f.Close()
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err := BuildFAT16FromDir(src, 16<<20)
			if err == nil || !strings.Contains(err.Error(), "budget") {
				t.Fatalf("BuildFAT16FromDir() error = %v, want budget error", err)
			}
		})
	}
}

// TestSDCardDirTick checks a dirty directory-backed card syncs on its timer.
func TestSDCardDirTick(t *testing.T) {
	dst := t.TempDir()
	card, err := NewSDCard(rp2350.New(), -1, "", dst)
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	writeTestFile(t, src, "tick.txt", []byte("tick"))
	img, _, err := BuildFAT16FromDir(src, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	card.image = img
	card.markDirty()
	card.Chip.Cycles = card.syncAt
	card.Tick()
	if got := dirSnapshot(t, dst); got["tick.txt"] != "tick" {
		t.Fatalf("tick did not sync: %v", got)
	}
}

// TestSDCardWriteBackToDir writes one 512-byte block through the card write
// path and checks the backing file is updated after the quiet period.
func TestSDCardWriteBackToDir(t *testing.T) {
	host := t.TempDir()
	original := make([]byte, 1024)
	for i := range original {
		original[i] = 0xAA
	}
	writeTestFile(t, host, "data.bin", original)

	card, err := NewSDCard(rp2350.New(), -1, "", host)
	if err != nil {
		t.Fatal(err)
	}
	l, err := parseFAT16(card.image)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := readDirEntries(card.image, l, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	var file fatDirEntry
	for _, e := range entries {
		if e.name == "data.bin" {
			file = e
		}
	}
	if file.cluster < 2 {
		t.Fatalf("data.bin cluster = %d", file.cluster)
	}
	block := uint32(l.clusterOffset(file.cluster) / 512)

	// Simulate the firmware's single-block write.
	card.writeToken = true
	card.multiAddr = block
	card.writeBuf = nil
	card.writeCRC = 0
	card.writeData(0xFE)
	for range 512 {
		card.writeData(0xBB)
	}
	card.writeData(0)
	card.writeData(0)

	card.Chip.Cycles = card.syncAt
	card.Tick()
	got, err := os.ReadFile(filepath.Join(host, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(original) {
		t.Fatalf("size = %d, want %d", len(got), len(original))
	}
	for i := range 512 {
		if got[i] != 0xBB {
			t.Fatalf("byte %d = %#x, want 0xBB", i, got[i])
		}
	}
	for i := 512; i < len(got); i++ {
		if got[i] != 0xAA {
			t.Fatalf("byte %d = %#x, want 0xAA", i, got[i])
		}
	}
}

// TestSDCardDirBoot reads the boot sector through the SPI command engine,
// proving the firmware sees the directory-backed image.
func TestSDCardDirBoot(t *testing.T) {
	host := t.TempDir()
	writeTestFile(t, host, "hello.txt", []byte("hi"))
	card, err := NewSDCard(rp2350.New(), -1, "", host)
	if err != nil {
		t.Fatal(err)
	}
	// The card starts deselected; drive CS low for the transaction.
	if r := sdCommand(t, card, 0, 0); r != 0x01 {
		t.Fatalf("CMD0 = %#x", r)
	}
	sdCommand(t, card, 55, 0)
	if r := sdCommand(t, card, 41, 0x4000_0000); r != 0 {
		t.Fatalf("ACMD41 = %#x", r)
	}
	if r := sdCommand(t, card, 17, 0); r != 0 {
		t.Fatalf("CMD17 = %#x", r)
	}
	boot := sdReadData(t, card)
	if boot[510] != 0x55 || boot[511] != 0xAA {
		t.Fatalf("boot signature = %02x%02x", boot[510], boot[511])
	}
	if got := string(boot[54:62]); got != "FAT16   " {
		t.Fatalf("fs type = %q", got)
	}
}

// TestSyncExternalFATWriter lets mtools write a file and checks our parser
// mirrors it to the host directory. Skipped when mtools is unavailable.
func TestSyncExternalFATWriter(t *testing.T) {
	if _, err := exec.LookPath("mcopy"); err != nil {
		t.Skip("mcopy not installed")
	}
	src := t.TempDir()
	writeTestFile(t, src, "seed.txt", []byte("seed"))
	img, manifest, err := BuildFAT16FromDir(src, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	imgPath := filepath.Join(t.TempDir(), "card.img")
	if err := os.WriteFile(imgPath, img, 0o600); err != nil {
		t.Fatal(err)
	}
	newFile := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(newFile, []byte("from mtools"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("mcopy", "-i", imgPath, "-o", newFile, "::EXTERNAL.TXT").CombinedOutput(); err != nil {
		t.Fatalf("mcopy: %v\n%s", err, out)
	}
	img, err = os.ReadFile(imgPath)
	if err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	card, err := NewSDCard(rp2350.New(), -1, "", dst)
	if err != nil {
		t.Fatal(err)
	}
	card.image = img
	card.manifest = manifest
	if err := card.Sync(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "EXTERNAL.TXT"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from mtools" {
		t.Fatalf("EXTERNAL.TXT = %q", got)
	}
}

// TestBuildFAT16FromDirManyEntries exercises a directory that spans several
// clusters and carries long-name records.
func TestBuildFAT16FromDirManyEntries(t *testing.T) {
	src := t.TempDir()
	for i := range 80 {
		writeTestFile(t, src, filepath.Join("many", fmt.Sprintf("long file name %03d.txt", i)), []byte{byte(i)})
	}
	img, _, err := BuildFAT16FromDir(src, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	l, err := parseFAT16(img)
	if err != nil {
		t.Fatal(err)
	}
	root, err := readDirEntries(img, l, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	var many fatDirEntry
	for _, e := range root {
		if e.name == "many" {
			many = e
		}
	}
	if many.cluster < 2 {
		t.Fatal("many/ missing")
	}
	entries, err := readDirEntries(img, l, many.cluster, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 80 {
		t.Fatalf("many/ has %d entries, want 80", len(entries))
	}
	if _, err := exec.LookPath("fsck.fat"); err == nil {
		path := filepath.Join(t.TempDir(), "card.img")
		if err := os.WriteFile(path, img, 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("fsck.fat", "-n", path).CombinedOutput(); err != nil {
			t.Fatalf("fsck.fat: %v\n%s", err, out)
		}
	}
}
