// USB-MIDI + UART MIDI (31250 baud) echo. A C4 note is sent over USB every
// second so the emulator's MIDI monitor and UI keyboard have traffic to show.
package main

import (
	"machine"
	"time"

	"machine/usb/adc/midi"
)

func main() {
	uart := machine.UART1
	uart.Configure(machine.UARTConfig{BaudRate: 31250, TX: machine.GPIO4, RX: machine.GPIO5})

	m := midi.Port()
	m.SetRxHandler(func(b []byte) {
		// Echo the complete USB-MIDI event packet back to the host.
		_, _ = m.Write(b)
	})

	// Echo raw UART MIDI bytes. The emulator taps UART1 TX, so this also
	// makes the UART bridge bidirectional for the MIDI monitor.
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := uart.Read(buf)
			if err != nil || n == 0 {
				// Read does not block; yield so the other goroutines run.
				time.Sleep(time.Millisecond)
				continue
			}
			_, _ = uart.Write(buf[:n])
		}
	}()

	go func() {
		for {
			time.Sleep(500 * time.Millisecond)
			_ = m.NoteOn(0, 1, midi.C4, 64)
			time.Sleep(200 * time.Millisecond)
			_ = m.NoteOff(0, 1, midi.C4, 64)
		}
	}()

	for {
		time.Sleep(time.Second)
	}
}
