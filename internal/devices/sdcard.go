package devices

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"

	"github.com/akhenakh/rp2350sim/internal/rp2350"
)

// SDCard models a microSD card in SPI mode. Commands, the response queue and
// single/multi-block reads are enough for FatFs and the Pico SDK SD examples.
// The default backing store is a small FAT16 image with one text file, so
// firmware can mount the card without a host image.
type SDCard struct {
	Chip *rp2350.Chip
	CS   int

	image    []byte
	Dir      string
	manifest map[string]bool
	dirty    bool
	syncAt   uint64
	Logger   *slog.Logger

	// Command parser.
	cmdBuf [6]byte
	cmdLen int
	acmd   bool
	out    []byte

	// Card state.
	initialized bool
	highCap     bool

	// Read/write state.
	multiRead  bool
	multiWrite bool
	multiAddr  uint32
	writeToken bool
	writeBuf   []byte
	writeCRC   int
}

// NewSDCard creates an SPI card on cs. It is backed by a host directory when
// dir is non-empty (files are packed at startup and written back after card
// writes), by imagePath when non-empty, or by a generated FAT16 image.
func NewSDCard(chip *rp2350.Chip, cs int, imagePath, dir string) (*SDCard, error) {
	if imagePath != "" && dir != "" {
		return nil, fmt.Errorf("sd: image and directory backings are mutually exclusive")
	}
	var img []byte
	var manifest map[string]bool
	var err error
	if dir != "" {
		if err = PrepareSDDir(dir); err != nil {
			return nil, err
		}
		img, manifest, err = BuildFAT16FromDir(dir, 16<<20)
	} else {
		img, err = loadOrBuildImage(imagePath)
	}
	if err != nil {
		return nil, err
	}
	return &SDCard{Chip: chip, CS: cs, image: img, Dir: dir, manifest: manifest}, nil
}

// Source describes the backing store for the UI and diagnostics.
func (s *SDCard) Source() string {
	if s.Dir != "" {
		return s.Dir
	}
	return "built-in FAT16"
}

// markDirty schedules a host-directory sync after the card has been quiet for
// a moment, so a multi-block file write is mirrored once rather than per
// sector.
func (s *SDCard) markDirty() {
	if s.Dir == "" {
		return
	}
	s.dirty = true
	s.syncAt = s.Chip.Cycles + 7_500_000 // 50 ms at 150 MHz
}

// Tick writes a dirty card back to its host directory when the quiet period
// expires.
func (s *SDCard) Tick() {
	if !s.dirty || s.Dir == "" || s.Chip.Cycles < s.syncAt {
		return
	}
	if err := s.Sync(); err != nil {
		loggerOr(s.Logger).Warn("sd sync failed", slog.String("dir", s.Dir), slog.Any("err", err))
		s.dirty = false
	}
}

func loadOrBuildImage(path string) ([]byte, error) {
	if path == "" {
		return BuildFAT16Image(16 << 20), nil
	}
	img, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sd image: %w", err)
	}
	if len(img) == 0 || len(img)%512 != 0 {
		return nil, fmt.Errorf("sd image: size %d is not a multiple of 512", len(img))
	}
	return img, nil
}

// Blocks returns the card size in 512-byte blocks.
func (s *SDCard) Blocks() int { return len(s.image) / 512 }

// Inserted reports whether the card is present (it always is in the model).
func (s *SDCard) Inserted() bool { return true }

// Transfer clocks one byte through the card.
func (s *SDCard) Transfer(tx byte) byte {
	if s.CS >= 0 && s.Chip.Pins.Level(s.CS) {
		s.deselect()
		return 0xFF
	}
	// A multiple-block read keeps the data line busy, but the host may still
	// clock in CMD12 (STOP_TRANSMISSION) while CS is low. Do not prefetch the
	// next block when the byte starts a command frame, or the command would be
	// swallowed as data clocks.
	if len(s.out) == 0 && s.multiRead && s.cmdLen == 0 && (tx < 0x40 || tx > 0x7F) {
		if s.queueBlock(s.multiAddr) {
			s.multiAddr++
		} else {
			// Past the end of the card: stop the stream with an SPI data
			// error token instead of returning 0xFF forever.
			s.multiRead = false
			s.out = append(s.out, 0x08, 0xFF)
		}
	}
	if len(s.out) > 0 {
		v := s.out[0]
		s.out = s.out[1:]
		return v
	}
	if s.writeToken {
		return s.writeData(tx)
	}

	// Command bytes are clocked with MISO high.
	if tx >= 0x40 && tx <= 0x7F && s.cmdLen == 0 {
		s.cmdBuf[0] = tx
		s.cmdLen = 1
		return 0xFF
	}
	if s.cmdLen > 0 {
		s.cmdBuf[s.cmdLen] = tx
		s.cmdLen++
		if s.cmdLen == 6 {
			s.cmdLen = 0
			s.command()
		}
		return 0xFF
	}
	return 0xFF
}

func (s *SDCard) deselect() {
	s.cmdLen = 0
	s.multiRead = false
	s.multiWrite = false
	s.writeToken = false
	s.writeBuf = nil
	s.writeCRC = 0
	s.acmd = false
	s.out = s.out[:0]
}

func (s *SDCard) arg() uint32 {
	return binary.BigEndian.Uint32(s.cmdBuf[1:5])
}

func (s *SDCard) r1(status byte) { s.out = append(s.out, status) }

// command executes one complete 6-byte command frame.
func (s *SDCard) command() {
	cmd := s.cmdBuf[0] & 0x3F
	arg := s.arg()
	app := s.acmd
	s.acmd = false

	if app {
		switch cmd {
		case 41: // ACMD41: initialize
			s.initialized = true
			s.highCap = arg&0x4000_0000 != 0
			s.r1(0)
			return
		case 13: // ACMD13: SD status
			s.r1(0)
			s.queueData(make([]byte, 64))
			return
		case 51: // ACMD51: SCR
			s.r1(0)
			scr := make([]byte, 8)
			scr[0] = 0x02 // SD spec 2.00
			scr[1] = 0x35 // security + data width
			s.queueData(scr)
			return
		case 6: // ACMD6: bus width (SPI ignores)
			s.r1(0)
			return
		}
		s.r1(0x04) // illegal command
		return
	}

	switch cmd {
	case 0: // GO_IDLE_STATE
		s.initialized = false
		s.highCap = false
		s.multiRead = false
		s.r1(1)
	case 1: // SEND_OP_COND (MMC)
		s.initialized = true
		s.r1(0)
	case 8: // SEND_IF_COND
		s.r1(1)
		s.out = append(s.out, 0x00, 0x00, byte(arg>>8), byte(arg))
	case 9: // SEND_CSD
		s.r1(0)
		s.queueData(s.csd())
	case 10: // SEND_CID
		s.r1(0)
		s.queueData(s.cid())
	case 12: // STOP_TRANSMISSION
		s.multiRead = false
		s.multiWrite = false
		s.writeToken = false
		s.writeBuf = nil
		s.out = append(s.out, 0x00, 0xFF)
	case 13: // SEND_STATUS
		s.r1(0)
		s.out = append(s.out, 0x00, 0x00)
	case 16: // SET_BLOCKLEN (the SPI model always moves 512-byte blocks)
		s.r1(0)
	case 17: // READ_SINGLE_BLOCK
		if !s.checkAddr(arg) {
			s.r1(0x20)
			return
		}
		s.r1(0)
		s.queueBlock(s.blockAddr(arg))
	case 18: // READ_MULTIPLE_BLOCK
		if !s.checkAddr(arg) {
			s.r1(0x20)
			return
		}
		s.r1(0)
		s.multiRead = true
		s.multiAddr = s.blockAddr(arg)
	case 24: // WRITE_BLOCK
		if !s.checkAddr(arg) {
			s.r1(0x20)
			return
		}
		s.r1(0)
		s.multiAddr = s.blockAddr(arg)
		s.writeToken = true
		s.writeBuf = nil
		s.writeCRC = 0
	case 55: // APP_CMD
		s.acmd = true
		if s.initialized {
			s.r1(0)
		} else {
			s.r1(1)
		}
	case 58: // READ_OCR
		s.r1(0)
		ocr := uint32(0xC0FF8000) // busy done, 3.3 V, CCS when high capacity
		if !s.highCap {
			ocr &^= 1 << 30
		}
		s.out = append(s.out, byte(ocr>>24), byte(ocr>>16), byte(ocr>>8), byte(ocr))
	case 59: // CRC_ON_OFF
		s.r1(0)
	case 25: // WRITE_MULTIPLE_BLOCK
		s.r1(0)
		s.multiAddr = s.blockAddr(arg)
		s.multiWrite = true
		s.writeToken = true
		s.writeBuf = nil
		s.writeCRC = 0
	default:
		s.r1(0x04)
	}
}

func (s *SDCard) checkAddr(arg uint32) bool {
	return int(s.blockAddr(arg)) < s.Blocks()
}

func (s *SDCard) blockAddr(arg uint32) uint32 {
	if s.highCap {
		return arg
	}
	return arg / 512
}

// queueBlock emits the data token, one block and the CRC. It reports false
// when the block is outside the card.
func (s *SDCard) queueBlock(block uint32) bool {
	if int(block) >= s.Blocks() {
		return false
	}
	s.out = append(s.out, 0xFE)
	start := int(block) * 512
	s.out = append(s.out, s.image[start:start+512]...)
	s.out = append(s.out, 0xFF, 0xFF)
	return true
}

func (s *SDCard) queueData(data []byte) {
	s.out = append(s.out, 0xFE)
	s.out = append(s.out, data...)
	s.out = append(s.out, 0xFF, 0xFF)
}

// writeData consumes a WRITE_BLOCK or WRITE_MULTIPLE_BLOCK frame: the start
// token, one 512-byte block and its CRC, then a data response token followed
// by a short busy period. Multi-block writes accept one 0xFC token per block
// and end on 0xFD.
func (s *SDCard) writeData(tx byte) byte {
	if s.writeBuf == nil {
		switch {
		case s.multiWrite && tx == 0xFD: // stop transmission token
			s.multiWrite = false
			s.writeToken = false
			return 0xFF
		case s.multiWrite && tx != 0xFC:
			return 0xFF // wait for the next block token
		case !s.multiWrite && tx != 0xFE:
			return 0xFF
		}
		s.writeBuf = make([]byte, 0, 512)
		s.writeCRC = 0
		return 0xFF
	}
	if len(s.writeBuf) < 512 {
		s.writeBuf = append(s.writeBuf, tx)
		if len(s.writeBuf) == 512 {
			start := int(s.multiAddr) * 512
			if start+512 <= len(s.image) {
				copy(s.image[start:start+512], s.writeBuf)
				s.markDirty()
			}
		}
		return 0xFF
	}
	s.writeCRC++
	if s.writeCRC < 2 {
		return 0xFF
	}
	s.writeBuf = nil
	s.writeCRC = 0
	s.out = append(s.out, 0x05, 0x00, 0x00, 0xFF)
	if s.multiWrite {
		s.multiAddr++
		return 0xFF
	}
	s.writeToken = false
	return 0xFF
}

// csd returns a CSD v2.0 structure sized for the backing image.
func (s *SDCard) csd() []byte {
	csd := make([]byte, 16)
	// C_SIZE is 22 bits: (C_SIZE+1) * 512 KB = capacity.
	size := max(s.Blocks()/1024-1, 0)
	size = min(size, 0x3F_FFFF)
	csd[0] = 0x40 // CSD_STRUCTURE 2.0
	csd[1] = 0x0E // TAAC
	csd[2] = 0x00 // NSAC
	csd[3] = 0x32 // TRAN_SPEED 25 MHz
	csd[4] = 0x5B // CCC
	csd[5] = 0x59 // READ_BL_LEN 512, flags
	csd[6] = 0x00
	csd[7] = byte(size>>16) & 0x3F
	csd[8] = byte(size >> 8)
	csd[9] = byte(size)
	csd[10] = byte(size << 4)
	csd[11] = 0x80
	csd[12] = 0x0A
	csd[13] = 0x40
	csd[14] = 0x00
	csd[15] = 0x00
	return csd
}

func (s *SDCard) cid() []byte {
	cid := make([]byte, 16)
	cid[0] = 0x03 // manufacturer: SanDisk
	oid := "RP"
	copy(cid[1:3], oid)
	copy(cid[3:8], "2350S")
	cid[8] = 0x10
	cid[9] = 0x00
	cid[10] = 0x00
	cid[11] = 0x00
	cid[12] = 0x01
	cid[13] = 0x2A
	cid[14] = 0x00
	cid[15] = 0x01
	return cid
}

// BuildFAT16Image builds a mountable FAT16 volume of the given size with one
// file in the root directory. Sizes below 4 MiB fall back to 4 MiB.
func BuildFAT16Image(size int) []byte {
	const (
		sectorSize  = 512
		reserved    = 1
		numFATs     = 2
		rootEntries = 512
		rootSectors = rootEntries * 32 / sectorSize
		spc         = 1
	)
	if size < 4<<20 {
		size = 4 << 20
	}
	total := size / sectorSize

	fatSectors := 1
	for {
		data := total - reserved - numFATs*fatSectors - rootSectors
		clusters := data / spc
		need := ((clusters+2)*2 + sectorSize - 1) / sectorSize
		if need <= fatSectors {
			break
		}
		fatSectors = need
	}

	img := make([]byte, total*sectorSize)
	bs := img[:sectorSize]
	bs[0], bs[1], bs[2] = 0xEB, 0x3C, 0x90
	copy(bs[3:11], "RP2350SIM")
	binary.LittleEndian.PutUint16(bs[11:], sectorSize)
	bs[13] = spc
	binary.LittleEndian.PutUint16(bs[14:], reserved)
	bs[16] = numFATs
	binary.LittleEndian.PutUint16(bs[17:], rootEntries)
	if total < 0x10000 {
		binary.LittleEndian.PutUint16(bs[19:], uint16(total))
	} else {
		binary.LittleEndian.PutUint32(bs[32:], uint32(total))
	}
	bs[21] = 0xF8
	binary.LittleEndian.PutUint16(bs[22:], uint16(fatSectors))
	binary.LittleEndian.PutUint16(bs[24:], 63)
	binary.LittleEndian.PutUint16(bs[26:], 255)
	bs[36] = 0x80
	bs[38] = 0x29
	binary.LittleEndian.PutUint32(bs[39:], 0x2350_0002)
	copy(bs[43:54], "RP2350SIM  ")
	copy(bs[54:62], "FAT16   ")
	bs[510], bs[511] = 0x55, 0xAA

	fatStart := reserved * sectorSize
	fat := img[fatStart : fatStart+fatSectors*sectorSize]
	binary.LittleEndian.PutUint16(fat[0:], 0xFFF8)
	binary.LittleEndian.PutUint16(fat[2:], 0xFFFF)
	binary.LittleEndian.PutUint16(fat[4:], 0xFFFF) // cluster 2: EOF
	// Mirror the FAT.
	copy(img[(reserved+fatSectors)*sectorSize:], fat)

	rootStart := (reserved + numFATs*fatSectors) * sectorSize
	content := []byte("RP2350-Touch-LCD-2 microSD\r\n")
	entry := img[rootStart:]
	copy(entry[0:8], "HELLO   ")
	copy(entry[8:11], "TXT")
	entry[11] = 0x20                             // archive
	binary.LittleEndian.PutUint16(entry[26:], 2) // first cluster
	binary.LittleEndian.PutUint32(entry[28:], uint32(len(content)))
	// FAT timestamps: 2026-01-01 00:00:00.
	binary.LittleEndian.PutUint16(entry[24:], (46<<9)|(1<<5)|1) // 2026-01-01
	binary.LittleEndian.PutUint16(entry[22:], 0)

	dataStart := (reserved + numFATs*fatSectors + rootSectors) * sectorSize
	copy(img[dataStart:], content)

	return img
}
