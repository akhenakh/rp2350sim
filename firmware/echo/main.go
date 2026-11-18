// echo reads the console and writes every byte back, so console input can be
// exercised from the runner with -input or from the UI keyboard.
package main

import (
	"machine"
)

func main() {
	println("echo: ready")
	for {
		b, err := machine.Serial.ReadByte()
		if err != nil {
			continue
		}
		machine.Serial.WriteByte(b)
		if b == '\r' {
			machine.Serial.WriteByte('\n')
		}
	}
}
