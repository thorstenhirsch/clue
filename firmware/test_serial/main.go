// Minimal USB-CDC test: proves serial works, independent of all display/SPI code.
// Build: tinygo build -target=nicenano -o test_serial.uf2 .
package main

import (
	"machine"
	"time"
)

func main() {
	time.Sleep(2 * time.Second) // let USB enumerate
	for {
		machine.Serial.Write([]byte("HELLO\n"))
		// Also echo anything received
		for machine.Serial.Buffered() > 0 {
			b, err := machine.Serial.ReadByte()
			if err != nil {
				break
			}
			machine.Serial.Write([]byte{'>', b})
		}
		time.Sleep(1 * time.Second)
	}
}
