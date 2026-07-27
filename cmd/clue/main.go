package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thorstenhirsch/clue/provider"
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

	if err := activeProvider.CheckCredentials(); err != nil {
		log.Fatalf("Failed to load credentials: %v\nRun '%s' to authenticate first.", err, authCommand)
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
	if err := sendLine(port, "A:"+providerID); err != nil {
		return fmt.Errorf("provider handshake: %w", err)
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

	var lastPrimaryResetMin int64 = -99
	var lastWasError bool
	var nextInterval time.Duration

	pollFn := func() error {
		result, wasError, interval, err := poll(port, lastPrimaryResetMin, lastWasError)
		if err != nil {
			return err
		}
		lastPrimaryResetMin = result
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

func poll(port serial.Port, prevPrimaryResetMin int64, prevWasError bool) (int64, bool, time.Duration, error) {
	// This doubles as a host-presence heartbeat for the firmware's LED watchdog.
	if err := sendLine(port, "L:1"); err != nil {
		return prevPrimaryResetMin, prevWasError, intervalBW, err
	}
	if err := activeProvider.CheckCredentials(); err != nil {
		if !prevWasError {
			log.Printf("Failed to load credentials: %v — run '%s' to re-authenticate", err, authCommand)
		}
		if err := sendLine(port, "E"); err != nil {
			return prevPrimaryResetMin, true, intervalCredCheck, err
		}
		return prevPrimaryResetMin, true, intervalCredCheck, nil
	}

	usage, err := activeProvider.FetchUsage()
	if err != nil {
		if errors.Is(err, provider.ErrAuth) {
			if !prevWasError {
				log.Printf("Token expired or revoked — run '%s' to re-authenticate", authCommand)
			}
			if err := sendLine(port, "E"); err != nil {
				return prevPrimaryResetMin, true, intervalCredCheck, err
			}
			return prevPrimaryResetMin, true, intervalCredCheck, nil
		}
		log.Printf("API error: %v", err)
		return prevPrimaryResetMin, false, intervalBW, nil
	}

	if prevWasError {
		log.Println("Credentials refreshed, resuming")
	}

	primaryResetDay, primaryResetMin := localReset(usage.Primary.Reset)
	secondaryResetDay, secondaryResetMin := localReset(usage.Secondary.Reset)

	if primaryResetMin != prevPrimaryResetMin {
		if !usage.Primary.Reset.IsZero() {
			log.Printf("Primary window resets at %s", usage.Primary.Reset.Local().Format("Mon 15:04"))
		}
		if !usage.Secondary.Reset.IsZero() {
			log.Printf("Secondary window resets at %s", usage.Secondary.Reset.Local().Format("Mon 15:04"))
		}
	}

	msg := encodeUsage(usage, primaryResetDay, primaryResetMin, secondaryResetDay, secondaryResetMin)

	log.Printf("Primary: %d%%  Secondary: %d%%",
		usagePercent(usage.Primary), usagePercent(usage.Secondary))
	if err := sendLine(port, msg); err != nil {
		return prevPrimaryResetMin, false, intervalBW, err
	}

	interval := intervalBW
	h5pct := usagePercent(usage.Primary)
	w1pct := usagePercent(usage.Secondary)
	if h5pct >= 80 || w1pct >= 80 {
		interval = intervalTri
	}
	return primaryResetMin, false, interval, nil
}

func localReset(reset time.Time) (day, minute int64) {
	if reset.IsZero() {
		return -1, -1
	}
	local := reset.Local()
	return int64(local.Weekday()), int64(local.Hour()*60 + local.Minute())
}

func usagePercent(window provider.Window) int64 {
	if window.Limit <= 0 {
		return 0
	}
	pct := window.Used * 100 / window.Limit
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

func encodeUsage(usage *provider.Usage, primaryResetDay, primaryResetMin, secondaryResetDay, secondaryResetMin int64) string {
	values := [...]int64{
		usage.Primary.Used, usage.Primary.Limit,
		usage.Secondary.Used, usage.Secondary.Limit,
		primaryResetMin, secondaryResetDay, secondaryResetMin,
		primaryResetDay, usage.Primary.DurationMins, usage.Secondary.DurationMins,
	}
	var b strings.Builder
	b.WriteString("U")
	for _, value := range values {
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(value, 10))
	}
	return b.String()
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
