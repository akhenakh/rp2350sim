// Command rp2350sim runs RP2350 firmware headlessly.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/akh/rp2350sim/internal/firmware"
	"github.com/akh/rp2350sim/internal/m33"
	"github.com/akh/rp2350sim/internal/rp2350"
)

func main() {
	var (
		path       = flag.String("fw", "", "firmware file (.uf2, .elf or .bin)")
		steps      = flag.Uint64("steps", 50_000_000, "maximum cycles to execute")
		secs       = flag.Float64("seconds", 0, "virtual seconds to run (overrides -steps)")
		unhandled  = flag.Bool("unhandled", false, "report unmapped peripheral accesses at exit")
		tracePins  = flag.String("trace-pins", "", "comma separated GPIO numbers to log")
		stopOnMMIO = flag.Bool("strict-mmio", false, "halt on the first unmapped peripheral access")
		core1      = flag.Bool("core1", true, "start core 1")
		quiet      = flag.Bool("quiet", false, "only print the final summary")
		traceN     = flag.Int("trace", 0, "trace the first N instructions")
		traceLast  = flag.Bool("trace-last", false, "dump the last 64 instructions on a fault")
		bpStr      = flag.String("bp", "", "break at this address (hex)")
		watchStr   = flag.String("watch", "", "log writes to this address (hex)")
		bp2Str     = flag.String("bp2", "", "secondary trace address (hex)")
		pokeStr    = flag.String("poke", "", "comma separated addr=val pokes (hex) after boot")
	)
	flag.Parse()
	if *path == "" {
		flag.Usage()
		os.Exit(2)
	}

	img, err := firmware.Load(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		os.Exit(1)
	}
	if !*quiet {
		fmt.Printf("firmware: %s, %d segment(s), %d bytes, entry=%#08x\n", img.Format, len(img.Segments), img.Size(), img.Entry)
	}

	c := rp2350.New()
	for _, seg := range img.Segments {
		c.LoadSegment(seg.Addr, seg.Data)
	}
	c.Boot()
	if !*core1 {
		c.CPU1.Parked = true
	}

	if *tracePins != "" {
		wanted := map[int]bool{}
		for _, s := range strings.Split(*tracePins, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err == nil {
				wanted[n] = true
			}
		}
		c.Pins.OnChange = func(pin int, level bool) {
			if wanted[pin] {
				fmt.Printf("%10.3fms gp%d=%v\n", float64(c.NowMicros())/1000, pin, level)
			}
		}
	}

	if *traceN > 0 {
		n := 0
		c.CPU0.TracePC = func(cpu *m33.CPU, pc uint32, hw1 uint16) {
			if n >= *traceN {
				return
			}
			n++
			fmt.Printf("%08x %04x r0=%08x r1=%08x r2=%08x r3=%08x sp=%08x lr=%08x\n",
				pc, hw1, cpu.R[0], cpu.R[1], cpu.R[2], cpu.R[3], cpu.R[13], cpu.R[14])
		}
	}

	var bp uint64
	if *bpStr != "" {
		bp, _ = strconv.ParseUint(strings.TrimPrefix(*bpStr, "0x"), 16, 32)
	}
	var bp2 uint64
	if *bp2Str != "" {
		bp2, _ = strconv.ParseUint(strings.TrimPrefix(*bp2Str, "0x"), 16, 32)
	}
	var ring []string
	zeros := 0
	bpHits := 0
	if *traceLast {
		c.CPU0.TracePC = func(cpu *m33.CPU, pc uint32, hw1 uint16) {
			if bp2 != 0 && uint64(pc) == bp2 {
				fmt.Printf("hit2 %#08x r0=%08x r1=%08x r2=%08x r3=%08x r4=%08x r5=%08x r8=%08x sp=%08x lr=%08x\n", pc, cpu.R[0], cpu.R[1], cpu.R[2], cpu.R[3], cpu.R[4], cpu.R[5], cpu.R[8], cpu.R[13], cpu.R[14])
			}
			if bp != 0 && uint64(pc) == bp {
				fmt.Printf("break at %#08x r0=%08x r1=%08x r2=%08x r3=%08x r4=%08x r5=%08x r6=%08x r7=%08x r8=%08x r9=%08x r10=%08x r11=%08x sp=%08x lr=%08x\n", pc, cpu.R[0], cpu.R[1], cpu.R[2], cpu.R[3], cpu.R[4], cpu.R[5], cpu.R[6], cpu.R[7], cpu.R[8], cpu.R[9], cpu.R[10], cpu.R[11], cpu.R[13], cpu.R[14])
				bpHits++
				if bpHits >= 200 {
					c.Halted = true
					os.Exit(1)
				} else {
					return
				}
			}
			if pc == 0x1000183c {
				sp, ln := c.Read32(cpu.R[1]), c.Read32(cpu.R[1]+4)
				str := make([]byte, 0, ln)
				for i := uint32(0); i < ln && i < 128; i++ {
					str = append(str, c.Read8(sp+i))
				}
				fmt.Printf("PANIC type=%#08x data=%#08x strp=%#08x len=%d msg=%q\n", cpu.R[0], cpu.R[1], sp, ln, string(str))
				start := 0
				if len(ring) > 40 {
					start = len(ring) - 40
				}
				for _, l := range ring[start:] {
					fmt.Println("  ", l)
				}
				c.Halted = true
				os.Exit(1)
			}
			if hw1 == 0 {
				zeros++
			} else {
				zeros = 0
				line := fmt.Sprintf("%08x %04x r0=%08x r1=%08x r2=%08x r3=%08x sp=%08x lr=%08x", pc, hw1, cpu.R[0], cpu.R[1], cpu.R[2], cpu.R[3], cpu.R[13], cpu.R[14])
				if len(ring) < 4096 {
					ring = append(ring, line)
				} else {
					copy(ring, ring[1:])
					ring[4095] = line
				}
			}
			if zeros > 64 {
				start := 0
				if len(ring) > 60 {
					start = len(ring) - 60
				}
				for _, l := range ring[start:] {
					fmt.Println("  ", l)
				}
				c.Halted = true
				os.Exit(1)
			}
		}
	}

	for _, kv := range strings.Split(*pokeStr, ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		a, _ := strconv.ParseUint(strings.TrimPrefix(parts[0], "0x"), 16, 32)
		v, _ := strconv.ParseUint(strings.TrimPrefix(parts[1], "0x"), 16, 32)
		c.Write32(uint32(a), uint32(v))
	}

	if *watchStr != "" {
		wa, _ := strconv.ParseUint(strings.TrimPrefix(*watchStr, "0x"), 16, 32)
		c.OnWrite = func(addr uint32, val uint32, size int) {
			if uint64(addr) == wa {
				fmt.Printf("write %#08x = %#08x (size %d) pc=%#08x lr=%#08x\n", addr, val, size, c.CPU0.R[15], c.CPU0.R[14])
			}
		}
	}

	if *stopOnMMIO {
		c.OnUnhandled = func(addr uint32, write bool, val uint32) {
			op := "read"
			if write {
				op = "write"
			}
			c.Halted = true
			fmt.Fprintf(os.Stderr, "unmapped %s at %#08x val=%#08x pc=%#08x\n", op, addr, val, c.CPU0.R[15])
			for _, l := range ring {
				fmt.Fprintln(os.Stderr, "  ", l)
			}
		}
	}

	start := time.Now()
	if *secs > 0 {
		c.Run(uint64(*secs * 150_000_000))
	} else {
		c.Run(*steps)
	}
	wall := time.Since(start)

	fmt.Printf("cycles=%d (%.3fs virtual) wall=%s\n", c.Cycles, float64(c.Cycles)/150e6, wall)
	fmt.Printf("core0 pc=%#08x sp=%#08x lr=%#08x\n", c.CPU0.R[15], c.CPU0.R[13], c.CPU0.R[14])
	fmt.Printf("core1 pc=%#08x sp=%#08x lr=%#08x parked=%v\n", c.CPU1.R[15], c.CPU1.R[13], c.CPU1.R[14], c.CPU1.Parked)
	fmt.Printf("runqueue head=%#08x tail=%#08x sleepQueue=%#08x currentTask=%#08x mainExited=%d\n",
		c.Read32(0x200017c0), c.Read32(0x200017c4), c.Read32(0x2000179c), c.Read32(0x2000175c), c.Read32(0x20001758))
	if c.CPU0.Halted {
		fmt.Printf("core0 halted: %s pc=%#08x lr=%#08x sp=%#08x\n", c.CPU0.HaltReason, c.CPU0.R[15], c.CPU0.R[14], c.CPU0.R[13])
	}
	if c.CPU1.Halted {
		fmt.Printf("core1 halted: %s pc=%#08x lr=%#08x sp=%#08x\n", c.CPU1.HaltReason, c.CPU1.R[15], c.CPU1.R[14], c.CPU1.R[13])
	}
	if *unhandled {
		fmt.Println("unmapped accesses:")
		fmt.Print(c.DescribeUnhandled())
	}
}
