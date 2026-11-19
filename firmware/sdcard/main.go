// Read the SPI microSD card on the RP2350-GEEK and print its identity, the
// FAT16 boot sector fields and the root directory. This exercises the stock
// TinyGo sdcard driver against the emulator's card model, including a host
// directory packed into the FAT image.
package main

import (
	"encoding/binary"
	"machine"
	"time"

	"tinygo.org/x/drivers/sdcard"
)

var uart = machine.UART0

func logf(s string) {
	uart.Write([]byte(s))
	uart.Write([]byte("\r\n"))
}

func hex32(v uint32) string {
	const digits = "0123456789abcdef"
	var buf [8]byte
	for i := 7; i >= 0; i-- {
		buf[i] = digits[v&0xf]
		v >>= 4
	}
	return string(buf[:])
}

// fatName renders an 8.3 directory entry name.
func fatName(e []byte) string {
	name := make([]byte, 0, 12)
	for _, c := range e[0:8] {
		if c == ' ' {
			break
		}
		name = append(name, c)
	}
	ext := e[8:11]
	if ext[0] != ' ' {
		name = append(name, '.')
		for _, c := range ext {
			if c == ' ' {
				break
			}
			name = append(name, c)
		}
	}
	return string(name)
}

// lfnPiece decodes the 13 UTF-16 characters of a VFAT long-name entry.
func lfnPiece(e []byte) string {
	var out []byte
	for _, span := range [][2]int{{1, 11}, {14, 26}, {28, 32}} {
		for i := span[0]; i < span[1]; i += 2 {
			c := uint16(e[i]) | uint16(e[i+1])<<8
			if c == 0 || c == 0xFFFF {
				continue
			}
			if c < 0x80 {
				out = append(out, byte(c))
			} else {
				out = append(out, '?')
			}
		}
	}
	return string(out)
}

func trimRight(s string) string {
	i := len(s)
	for i > 0 && s[i-1] == ' ' {
		i--
	}
	return s[:i]
}

func main() {
	spi := machine.SPI0
	dev := sdcard.New(spi, machine.GPIO18, machine.GPIO19, machine.GPIO20, machine.GPIO23)
	if err := dev.Configure(); err != nil {
		logf("sd: no card: " + err.Error())
		return
	}

	kind := "SD1"
	if dev.CSD.CSD_STRUCTURE == 1 {
		kind = "SDHC/SDXC"
	} else if dev.CID.ManufacturerID != 0 {
		kind = "SD2"
	}
	mb := int((dev.CSD.C_SIZE + 1) / 2) // (C_SIZE+1) * 512 KiB
	logf("sd: " + kind + " card, mfr 0x" + hex32(uint32(dev.CID.ManufacturerID)) +
		", serial 0x" + hex32(dev.CID.ProductSerialNumber) + ", " + itoa(mb) + " MB")

	var boot [512]byte
	if err := dev.ReadData(0, boot[:]); err != nil {
		logf("sd: boot sector read failed: " + err.Error())
		return
	}

	bytesPerSector := binary.LittleEndian.Uint16(boot[11:])
	sectorsPerCluster := boot[13]
	reserved := binary.LittleEndian.Uint16(boot[14:])
	fatCount := boot[16]
	rootEntries := binary.LittleEndian.Uint16(boot[17:])
	sectorsPerFat := binary.LittleEndian.Uint16(boot[22:])
	label := string(boot[43:54])
	logf("fat: " + trimRight(string(boot[54:62])) + " · " + itoa(int(bytesPerSector)) + "-byte sectors · " +
		itoa(int(sectorsPerCluster)) + " sectors/cluster · label \"" + trimRight(label) + "\"")

	rootSector := uint32(reserved) + uint32(fatCount)*uint32(sectorsPerFat)
	rootBytes := uint32(rootEntries) * 32
	rootSectors := (rootBytes + uint32(bytesPerSector) - 1) / uint32(bytesPerSector)
	files := 0
	var entry [32]byte
	lfn := ""
	for s := range rootSectors {
		var sector [512]byte
		if err := dev.ReadData(rootSector+s, sector[:]); err != nil {
			logf("sd: root read failed: " + err.Error())
			return
		}
		for i := 0; i < 512; i += 32 {
			copy(entry[:], sector[i:i+32])
			if entry[0] == 0 {
				break
			}
			if entry[11]&0x3F == 0x0F { // long-name fragment, stored in reverse
				lfn = lfnPiece(entry[:]) + lfn
				continue
			}
			if entry[0] == 0xE5 || entry[11]&0x08 != 0 { // deleted or volume label
				lfn = ""
				continue
			}
			name := lfn
			if name == "" {
				name = fatName(entry[:])
			}
			lfn = ""
			size := binary.LittleEndian.Uint32(entry[28:])
			if entry[11]&0x10 != 0 {
				logf("dir:  " + name + "/")
				continue
			}
			logf("file: " + name + " " + itoa(int(size)) + " bytes")
			files++
		}
	}
	logf("sd: " + itoa(files) + " file(s) in the root directory")

	// Keep the machine alive so the UI and the smoke test can observe it.
	led := machine.GPIO25
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})
	on := false
	for {
		led.Set(on)
		on = !on
		time.Sleep(500 * time.Millisecond)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
