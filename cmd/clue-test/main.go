package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.bug.st/serial"
)

type step struct {
	label string
	h5pct int
	w1pct int
	err   bool
	full  bool // expect full OTP refresh (longer delay)
	blink int  // expected LED blinks (0=none, 3=h5@80%, 5=any@100%)
}

func main() {
	portFlag := flag.String("port", "", "serial port (e.g. /dev/ttyACM0); auto-detected if empty")
	pauseFlag := flag.Duration("pause", 3*time.Second, "viewing pause after refresh completes")
	flag.Parse()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	portName := *portFlag
	if portName == "" {
		log.Println("Waiting for device...")
		for {
			select {
			case sig := <-sigCh:
				log.Printf("Received %s", sig)
				return
			default:
			}
			name, err := detectPort()
			if err == nil {
				portName = name
				break
			}
			time.Sleep(2 * time.Second)
		}
	}

	log.Printf("Opening %s", portName)
	port, err := serial.Open(portName, &serial.Mode{
		BaudRate:          115200,
		InitialStatusBits: &serial.ModemOutputBits{DTR: true, RTS: true},
	})
	if err != nil {
		log.Fatalf("Failed to open serial port: %v", err)
	}
	defer port.Close()
	port.SetDTR(true)
	port.SetRTS(true)
	port.SetReadTimeout(200 * time.Millisecond)

	log.Println("Waiting for handshake...")
	sendLine(port, "G")
	if !handshake(port, sigCh) {
		return
	}
	log.Println("Device connected")

	limit := 1_000_000

	steps := []step{
		// --- Sequence 1: full lifecycle ---
		// LED is on from boot. First reading suppresses blink even though
		// it's a "new" value — the sentinel H5Limit=-1 guard prevents it.
		{label: "init 10/5 (expect: full, LED: no blink)", h5pct: 10, w1pct: 5, full: true},
		{label: "update 50/20 (expect: bw)", h5pct: 50, w1pct: 20},
		{label: "update 85/20 — h5 turns red (expect: full, LED: 3 blinks)", h5pct: 85, w1pct: 20, full: true, blink: 3},
		{label: "update 90/23 — h5 red grows (expect: tri, LED: no blink)", h5pct: 90, w1pct: 23},
		{label: "update 95/85 — w1 turns red (expect: full, LED: no blink)", h5pct: 95, w1pct: 85, full: true},
		{label: "update 100/87 — h5 hits 100 (expect: tri, LED: 5 blinks)", h5pct: 100, w1pct: 87, blink: 5},
		{label: "reset 0/0 — red cleared (expect: full)", h5pct: 0, w1pct: 0, full: true},
		{label: "update 20/85 — w1 turns red (expect: full)", h5pct: 20, w1pct: 85, full: true},
		{label: "update 50/89 — w1 red grows (expect: tri)", h5pct: 50, w1pct: 89},
		{label: "reset 10/3 — red cleared (expect: full)", h5pct: 10, w1pct: 3, full: true},
		{label: "update 50/17 (expect: bw)", h5pct: 50, w1pct: 17},

		// --- Error screen ---
		{label: "auth error (expect: bw)", err: true},

		// --- Sequence 2: start above 80% ---
		{label: "init 85/15 — h5 turns red (expect: full, LED: 3 blinks)", h5pct: 85, w1pct: 15, full: true, blink: 3},
		{label: "update 90/17 — h5 red grows (expect: tri)", h5pct: 90, w1pct: 17},

		// --- Edge cases ---
		{label: "update 80/17 — h5 exactly 80% (expect: tri)", h5pct: 80, w1pct: 17},
		{label: "update 79/17 — h5 drops below 80% (expect: full)", h5pct: 79, w1pct: 17, full: true},
		{label: "update 80/80 — both turn red (expect: full, LED: 3 blinks)", h5pct: 80, w1pct: 80, full: true, blink: 3},
		{label: "update 81/81 — both grow (expect: tri)", h5pct: 81, w1pct: 81},
		{label: "update 50/50 — both drop (expect: full)", h5pct: 50, w1pct: 50, full: true},

		// --- LED blink tests ---
		// Reset to low baseline, then test each blink trigger in isolation.
		{label: "LED: baseline 10/5 (expect: bw, LED: no blink)", h5pct: 10, w1pct: 5},
		{label: "LED: h5 crosses 80% (expect: full, LED: 3 blinks)", h5pct: 80, w1pct: 5, full: true, blink: 3},
		{label: "LED: h5 crosses 100% (expect: tri, LED: 5 blinks)", h5pct: 100, w1pct: 5, blink: 5},
		{label: "LED: reset (expect: full)", h5pct: 5, w1pct: 5, full: true},
		{label: "LED: w1 crosses 100% (expect: full, LED: 5 blinks)", h5pct: 5, w1pct: 100, full: true, blink: 5},
		{label: "LED: reset to 0/0 (expect: full)", h5pct: 0, w1pct: 0, full: true},
		{label: "LED: big jump 0→100 h5 (expect: full, LED: 5 blinks)", h5pct: 100, w1pct: 0, full: true, blink: 5},
	}

	for i, s := range steps {
		select {
		case sig := <-sigCh:
			log.Printf("Received %s, stopping", sig)
			return
		default:
		}

		blinkNote := ""
		if s.blink > 0 {
			blinkNote = fmt.Sprintf(" [LED: %d blinks]", s.blink)
		}
		log.Printf("[%d/%d] %s%s", i+1, len(steps), s.label, blinkNote)

		if s.err {
			sendLine(port, "E")
		} else {
			used5 := int64(s.h5pct) * int64(limit) / 100
			used1 := int64(s.w1pct) * int64(limit) / 100
			msg := "U:" +
				strconv.FormatInt(used5, 10) + ":" +
				strconv.Itoa(limit) + ":" +
				strconv.FormatInt(used1, 10) + ":" +
				strconv.Itoa(limit) + ":" +
				"870:3:870"
			sendLine(port, msg)
		}

		refreshDelay := 10 * time.Second // quick refresh (bw/tri)
		if s.full {
			refreshDelay = 20 * time.Second // full OTP refresh + register re-init
		}
		// blink() runs after refresh: 400ms per blink (200ms off + 200ms on)
		refreshDelay += time.Duration(s.blink) * 400 * time.Millisecond
		drainResponse(port, refreshDelay)
		time.Sleep(*pauseFlag)
	}

	log.Println("=== TEST COMPLETE ===")
}

func sendLine(port serial.Port, s string) {
	port.Write([]byte(s + "\n"))
}

func handshake(port serial.Port, sigCh <-chan os.Signal) bool {
	var rxBuf [256]byte
	rxN := 0
	lastG := time.Now()
	for {
		select {
		case sig := <-sigCh:
			log.Printf("Received %s", sig)
			return false
		default:
		}
		n, _ := port.Read(rxBuf[rxN:])
		rxN += n
		for i := 0; i < rxN; i++ {
			if rxBuf[i] == '\n' || rxBuf[i] == '\r' {
				line := strings.TrimSpace(string(rxBuf[:i]))
				copy(rxBuf[:], rxBuf[i+1:rxN])
				rxN -= i + 1
				if line == "" {
					break
				}
				if line == "R" || line == "N" || strings.HasPrefix(line, "T:") {
					return true
				}
				break
			}
		}
		if time.Since(lastG) >= 2*time.Second {
			sendLine(port, "G")
			lastG = time.Now()
		}
	}
}

func drainResponse(port serial.Port, wait time.Duration) {
	var buf [512]byte
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		n, _ := port.Read(buf[:])
		if n > 0 {
			for _, line := range strings.Split(strings.TrimSpace(string(buf[:n])), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					log.Printf("  <- %s", line)
				}
			}
		}
	}
}

func detectPort() (string, error) {
	ports, err := serial.GetPortsList()
	if err != nil {
		return "", err
	}
	for _, p := range ports {
		if strings.Contains(p, "ttyACM") {
			return p, nil
		}
	}
	if len(ports) > 0 {
		return ports[0], nil
	}
	return "", fmt.Errorf("no serial ports detected")
}
