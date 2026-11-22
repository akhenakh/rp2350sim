package devices

import (
	"bufio"
	"fmt"
	"io"
	"time"
)

// WriteVCD exports a pin trace as an IEEE 1364 value change dump. Times are
// derived from the virtual 150 MHz clock and expressed in nanoseconds, so the
// file opens directly in GTKWave or PulseView. Only digital states are
// recorded; analog scope values are thresholded like TraceSample.Bit.
func WriteVCD(w io.Writer, pins []int, samples []TraceSample, interval uint64) error {
	if len(pins) > MaxTraceChannels {
		pins = pins[:MaxTraceChannels]
	}
	bw := bufio.NewWriter(w)
	var firstErr error
	emit := func(format string, args ...any) {
		if firstErr != nil {
			return
		}
		_, firstErr = fmt.Fprintf(bw, format, args...)
	}
	emit("$date %s $end\n", time.Now().Format("2006-01-02 15:04:05 -0700"))
	emit("$version rp2350sim $end\n")
	emit("$timescale 1ns $end\n")
	if interval > 0 {
		emit("$comment sample interval %d ns $end\n", (interval*20+1)/3)
	}
	emit("$scope module logic $end\n")
	ids := make([]byte, len(pins))
	for i, pin := range pins {
		ids[i] = byte('!') + byte(i)
		emit("$var wire 1 %c GP%d $end\n", ids[i], pin)
	}
	emit("$upscope $end\n$enddefinitions $end\n")
	if len(samples) == 0 || len(pins) == 0 {
		if firstErr != nil {
			return firstErr
		}
		return bw.Flush()
	}

	base := samples[0].T
	ns := func(cycles uint64) uint64 {
		// 150 MHz = 20/3 ns per cycle, rounded to the nearest nanosecond.
		return (cycles*20 + 1) / 3
	}
	emit("#%d\n$dumpvars\n", ns(samples[0].T-base))
	for i := range pins {
		emit("%d%c\n", bitInt(samples[0].Bit(i)), ids[i])
	}
	emit("$end\n")

	last := make([]bool, len(pins))
	for i := range pins {
		last[i] = samples[0].Bit(i)
	}
	for _, s := range samples[1:] {
		at := ns(s.T - base)
		header := false
		for i := range pins {
			bit := s.Bit(i)
			if bit == last[i] {
				continue
			}
			if !header {
				emit("#%d\n", at)
				header = true
			}
			emit("%d%c\n", bitInt(bit), ids[i])
			last[i] = bit
		}
	}
	if firstErr != nil {
		return firstErr
	}
	return bw.Flush()
}

func bitInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
