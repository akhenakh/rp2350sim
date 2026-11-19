package devices

import (
	"encoding/binary"
	"fmt"
)

// FAT16 on-disk structures shared by the in-memory card builder and the
// host-directory synchroniser. Only the fields this emulator needs are
// modelled: fixed 512-byte sectors, 8.3 names plus VFAT long names, and
// ordinary files and directories.

const (
	fatSectorSize = 512
	fatReserved   = 1
	fatNumFATs    = 2
	fatRootEnts   = 512
	fatSpc        = 4 // 2 KiB clusters, 16 MiB card
)

type fat16Layout struct {
	sectorSize   int
	spc          int
	reserved     int
	numFATs      int
	rootEntries  int
	totalSectors int
	fatSectors   int
	rootOffset   int
	dataOffset   int
}

func (l *fat16Layout) clusterSize() int { return l.spc * l.sectorSize }

func (l *fat16Layout) dataSectors() int {
	return l.totalSectors - l.reserved - l.numFATs*l.fatSectors - l.rootEntries*32/l.sectorSize
}

// clusterCount is the number of data clusters (clusters 2..N+1).
func (l *fat16Layout) clusterCount() int { return l.dataSectors() / l.spc }

func (l *fat16Layout) clusterOffset(cluster int) int {
	return l.dataOffset + (cluster-2)*l.clusterSize()
}

func (l *fat16Layout) fatOffset() int { return l.reserved * l.sectorSize }

func (l *fat16Layout) fatEntry(img []byte, cluster int) (int, error) {
	if cluster < 0 || cluster >= l.clusterCount()+2 {
		return 0, fmt.Errorf("fat: cluster %d out of range", cluster)
	}
	off := l.fatOffset() + cluster*2
	if off+2 > len(img) {
		return 0, fmt.Errorf("fat: FAT entry outside image")
	}
	return int(binary.LittleEndian.Uint16(img[off:])), nil
}

func (l *fat16Layout) setFATEntry(img []byte, cluster, next int) {
	off := l.fatOffset() + cluster*2
	for f := 0; f < l.numFATs; f++ {
		o := off + f*l.fatSectors*l.sectorSize
		binary.LittleEndian.PutUint16(img[o:], uint16(next))
	}
}

// parseFAT16 validates the boot sector and returns the layout.
func parseFAT16(img []byte) (*fat16Layout, error) {
	if len(img) < fatSectorSize {
		return nil, fmt.Errorf("fat: image too small")
	}
	l := &fat16Layout{}
	l.sectorSize = int(binary.LittleEndian.Uint16(img[11:]))
	l.spc = int(img[13])
	l.reserved = int(binary.LittleEndian.Uint16(img[14:]))
	l.numFATs = int(img[16])
	l.rootEntries = int(binary.LittleEndian.Uint16(img[17:]))
	total16 := int(binary.LittleEndian.Uint16(img[19:]))
	total32 := int(binary.LittleEndian.Uint32(img[32:]))
	l.fatSectors = int(binary.LittleEndian.Uint16(img[22:]))
	if total16 != 0 {
		l.totalSectors = total16
	} else {
		l.totalSectors = total32
	}
	if l.sectorSize != fatSectorSize || l.spc == 0 || l.numFATs == 0 ||
		l.rootEntries == 0 || l.fatSectors == 0 || l.totalSectors == 0 {
		return nil, fmt.Errorf("fat: unsupported boot sector")
	}
	if l.spc&(l.spc-1) != 0 {
		return nil, fmt.Errorf("fat: sectors per cluster %d is not a power of two", l.spc)
	}
	l.rootOffset = (l.reserved + l.numFATs*l.fatSectors) * l.sectorSize
	l.dataOffset = l.rootOffset + l.rootEntries*32
	if l.dataOffset > len(img) || l.totalSectors*l.sectorSize > len(img) {
		return nil, fmt.Errorf("fat: geometry exceeds image")
	}
	if c := l.clusterCount(); c < 4085 || c > 65524 {
		return nil, fmt.Errorf("fat: %d clusters is not FAT16", c)
	}
	return l, nil
}

// buildLayout chooses the FAT size for a fixed geometry. The data area and
// FAT size depend on each other, so iterate until the FAT covers the clusters.
func buildLayout(totalSectors int) (*fat16Layout, error) {
	rootSectors := fatRootEnts * 32 / fatSectorSize
	fatSectors := 1
	for ; ; fatSectors++ {
		data := totalSectors - fatReserved - fatNumFATs*fatSectors - rootSectors
		if data <= 0 {
			return nil, fmt.Errorf("fat: image too small")
		}
		clusters := data / fatSpc
		need := ((clusters+2)*2 + fatSectorSize - 1) / fatSectorSize
		if need <= fatSectors {
			break
		}
	}
	l := &fat16Layout{
		sectorSize:   fatSectorSize,
		spc:          fatSpc,
		reserved:     fatReserved,
		numFATs:      fatNumFATs,
		rootEntries:  fatRootEnts,
		totalSectors: totalSectors,
		fatSectors:   fatSectors,
	}
	l.rootOffset = (l.reserved + l.numFATs*l.fatSectors) * l.sectorSize
	l.dataOffset = l.rootOffset + l.rootEntries*32
	if c := l.clusterCount(); c < 4085 || c > 65524 {
		return nil, fmt.Errorf("fat: %d clusters is not FAT16", c)
	}
	return l, nil
}

func writeBootSector(img []byte, l *fat16Layout) {
	bs := img[:l.sectorSize]
	bs[0], bs[1], bs[2] = 0xEB, 0x3C, 0x90
	copy(bs[3:11], "RP2350SIM")
	binary.LittleEndian.PutUint16(bs[11:], uint16(l.sectorSize))
	bs[13] = byte(l.spc)
	binary.LittleEndian.PutUint16(bs[14:], uint16(l.reserved))
	bs[16] = byte(l.numFATs)
	binary.LittleEndian.PutUint16(bs[17:], uint16(l.rootEntries))
	if l.totalSectors < 0x10000 {
		binary.LittleEndian.PutUint16(bs[19:], uint16(l.totalSectors))
	} else {
		binary.LittleEndian.PutUint32(bs[32:], uint32(l.totalSectors))
	}
	bs[21] = 0xF8
	binary.LittleEndian.PutUint16(bs[22:], uint16(l.fatSectors))
	binary.LittleEndian.PutUint16(bs[24:], 63)
	binary.LittleEndian.PutUint16(bs[26:], 255)
	bs[36] = 0x80
	bs[38] = 0x29
	binary.LittleEndian.PutUint32(bs[39:], 0x2350_0002)
	copy(bs[43:54], "RP2350SIM  ")
	copy(bs[54:62], "FAT16   ")
	bs[510], bs[511] = 0x55, 0xAA
}
