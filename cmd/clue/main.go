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

	"github.com/thorstenhirsch/clue/claude"
	"go.bug.st/serial"
)

const (
	intervalCredCheck = 2 * time.Second
	intervalBW        = 10 * time.Second
	intervalTri       = 15 * time.Second
	intervalFull      = 30 * time.Second
)

func main() {
	portFlag := flag.String("port", "", "serial port (e.g. /dev/ttyACM0); auto-detected if empty")
	flag.Parse()

	if _, err := claude.LoadCredentials(); err != nil {
		log.Fatalf("Failed to load credentials: %v\nRun 'claude' to authenticate first.", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	for {
		portName, ok := waitForPort(*portFlag, sigCh)
		if !ok {
			return
		}

		if err := runSession(portName, sigCh); err != nil {
			log.Printf("Device disconnected: %v", err)
			continue
		}
		return
	}
}

func waitForPort(explicit string, sigCh <-chan os.Signal) (string, bool) {
	log.Println("Waiting for device...")
	for {
		select {
		case sig := <-sigCh:
			log.Printf("Received %s, shutting down", sig)
			return "", false
		default:
		}
		if explicit != "" {
			if _, err := os.Stat(explicit); err == nil {
				return explicit, true
			}
		} else {
			if name, err := detectPort(); err == nil {
				return name, true
			}
		}
		time.Sleep(2 * time.Second)
	}
}

func runSession(portName string, sigCh <-chan os.Signal) error {
	log.Printf("Opening %s", portName)
	port, err := serial.Open(portName, &serial.Mode{
		BaudRate:          115200,
		InitialStatusBits: &serial.ModemOutputBits{DTR: true, RTS: true},
	})
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer port.Close()
	port.SetDTR(true)
	port.SetRTS(true)
	port.SetReadTimeout(200 * time.Millisecond)

	log.Println("Waiting for handshake...")
	if err := sendLine(port, "G"); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	var rxBuf [256]byte
	rxN := 0
	lastG := time.Now()
	connected := false
	for !connected {
		select {
		case sig := <-sigCh:
			log.Printf("Received %s, shutting down", sig)
			return nil
		default:
		}
		n, err := port.Read(rxBuf[rxN:])
		if err != nil {
			return fmt.Errorf("handshake read: %w", err)
		}
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
					log.Println("Device connected")
					connected = true
				}
				break
			}
		}
		if !connected && time.Since(lastG) >= 2*time.Second {
			if err := sendLine(port, "G"); err != nil {
				return fmt.Errorf("handshake: %w", err)
			}
			lastG = time.Now()
		}
	}

	disconnected := make(chan struct{})
	go func() {
		defer close(disconnected)
		var buf [256]byte
		for {
			if _, err := port.Read(buf[:]); err != nil {
				return
			}
		}
	}()

	refreshTimer := time.NewTimer(time.Until(next4AM()))
	defer refreshTimer.Stop()

	var lastH5ResetMin int64 = -99
	var lastWasError bool
	var nextInterval time.Duration

	pollFn := func() error {
		result, wasError, interval, err := poll(port, lastH5ResetMin, lastWasError)
		if err != nil {
			return err
		}
		lastH5ResetMin = result
		lastWasError = wasError
		nextInterval = interval
		return nil
	}

	if err := pollFn(); err != nil {
		return err
	}
	nextInterval = intervalFull
	log.Printf("Next poll in %s", nextInterval)

	pollTimer := time.NewTimer(nextInterval)
	defer pollTimer.Stop()

	for {
		select {
		case <-pollTimer.C:
			if err := pollFn(); err != nil {
				return err
			}
			log.Printf("Next poll in %s", nextInterval)
			pollTimer.Reset(nextInterval)
		case <-refreshTimer.C:
			log.Println("Nightly full refresh")
			if err := sendLine(port, "F"); err != nil {
				return err
			}
			pollTimer.Reset(intervalFull)
			refreshTimer.Reset(time.Until(next4AM()))
		case <-disconnected:
			return fmt.Errorf("device gone")
		case sig := <-sigCh:
			log.Printf("Received %s, shutting down", sig)
			sendLine(port, "L:0")
			return nil
		}
	}
}

func poll(port serial.Port, prevH5ResetMin int64, prevWasError bool) (int64, bool, time.Duration, error) {
	creds, err := claude.LoadCredentials()
	if err != nil {
		if !prevWasError {
			log.Printf("Failed to load credentials: %v", err)
		}
		if err := sendLine(port, "E"); err != nil {
			return prevH5ResetMin, true, intervalCredCheck, err
		}
		return prevH5ResetMin, true, intervalCredCheck, nil
	}

	client := claude.NewClient(creds.AccessToken)
	usage, err := client.FetchUsage()
	if err != nil {
		if _, ok := err.(claude.ErrAuth); ok {
			if !prevWasError {
				log.Println("Token expired or revoked — run 'claude' to re-authenticate")
			}
			if err := sendLine(port, "E"); err != nil {
				return prevH5ResetMin, true, intervalCredCheck, err
			}
			return prevH5ResetMin, true, intervalCredCheck, nil
		}
		log.Printf("API error: %v", err)
		return prevH5ResetMin, false, intervalBW, nil
	}

	if prevWasError {
		log.Println("Credentials refreshed, resuming")
	}

	h5resetMin := int64(-1)
	w1resetDay := int64(-1)
	w1resetMin := int64(-1)
	if !usage.H5Reset.IsZero() {
		local := usage.H5Reset.Local()
		h5resetMin = int64(local.Hour()*60 + local.Minute())
	}
	if !usage.W1Reset.IsZero() {
		local := usage.W1Reset.Local()
		w1resetDay = int64(local.Weekday())
		w1resetMin = int64(local.Hour()*60 + local.Minute())
	}

	if h5resetMin != prevH5ResetMin {
		if !usage.H5Reset.IsZero() {
			log.Printf("5h resets at %s", usage.H5Reset.Local().Format("15:04"))
		}
		if !usage.W1Reset.IsZero() {
			local := usage.W1Reset.Local()
			log.Printf("7d resets at %s %s", local.Weekday().String()[:3], local.Format("15:04"))
		}
	}

	msg := "U:" +
		strconv.FormatInt(usage.H5Used, 10) + ":" +
		strconv.FormatInt(usage.H5Limit, 10) + ":" +
		strconv.FormatInt(usage.W1Used, 10) + ":" +
		strconv.FormatInt(usage.W1Limit, 10) + ":" +
		strconv.FormatInt(h5resetMin, 10) + ":" +
		strconv.FormatInt(w1resetDay, 10) + ":" +
		strconv.FormatInt(w1resetMin, 10)

	log.Printf("5h: %d/%d  1w: %d/%d", usage.H5Used, usage.H5Limit, usage.W1Used, usage.W1Limit)
	if err := sendLine(port, msg); err != nil {
		return prevH5ResetMin, false, intervalBW, err
	}

	interval := intervalBW
	h5pct := usage.H5Used * 100 / usage.H5Limit
	w1pct := usage.W1Used * 100 / usage.W1Limit
	if h5pct >= 80 || w1pct >= 80 {
		interval = intervalTri
	}
	return h5resetMin, false, interval, nil
}

func sendLine(port serial.Port, s string) error {
	_, err := port.Write([]byte(s + "\n"))
	return err
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

func next4AM() time.Time {
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), 4, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next
}
