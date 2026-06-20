package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/torti/tiny-claude-eink-display/claude"
	"go.bug.st/serial"
)

func main() {
	portFlag := flag.String("port", "", "serial port (e.g. /dev/ttyACM0); auto-detected if empty")
	intervalFlag := flag.Duration("interval", 30*time.Second, "polling interval")
	flag.Parse()

	creds, err := claude.LoadCredentials()
	if err != nil {
		log.Fatalf("Failed to load credentials: %v\nRun 'claude' to authenticate first.", err)
	}
	if creds.Expired() {
		log.Fatalf("Access token expired at %s.\nRun 'claude' to refresh.", creds.ExpiresAt.Format(time.RFC3339))
	}
	log.Printf("Loaded %s credentials (expires %s)", creds.SubscriptionType, creds.ExpiresAt.Format(time.RFC3339))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	portName := *portFlag
	if portName == "" {
		log.Println("Waiting for device...")
		for {
			select {
			case sig := <-sigCh:
				log.Printf("Received %s, shutting down", sig)
				return
			default:
			}
			portName, err = detectPort()
			if err == nil {
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
	port.SetReadTimeout(1 * time.Second)

	scanner := bufio.NewScanner(port)
	scanner.Buffer(make([]byte, 8192), 8192)

	log.Println("Waiting for handshake...")
	sendLine(port, "G")
	connected := false
	for !connected {
		select {
		case sig := <-sigCh:
			log.Printf("Received %s, shutting down", sig)
			return
		default:
		}
		if scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "R" || line == "N" || strings.HasPrefix(line, "T:") {
				log.Println("Device connected")
				connected = true
			}
		}
		if !connected {
			sendLine(port, "G")
		}
	}

	client := claude.NewClient(creds.AccessToken)

	log.Printf("Polling every %s", *intervalFlag)
	ticker := time.NewTicker(*intervalFlag)
	defer ticker.Stop()

	refreshTimer := time.NewTimer(time.Until(next4AM()))
	defer refreshTimer.Stop()

	var lastH5ResetMin int64 = -99
	pollFn := func() {
		lastH5ResetMin = poll(client, port, lastH5ResetMin)
	}

	pollFn()
	for {
		select {
		case <-ticker.C:
			pollFn()
		case <-refreshTimer.C:
			log.Println("Nightly full refresh")
			sendLine(port, "F")
			pollFn()
			refreshTimer.Reset(time.Until(next4AM()))
		case sig := <-sigCh:
			log.Printf("Received %s, shutting down", sig)
			return
		}
	}
}

func poll(client *claude.Client, port serial.Port, prevH5ResetMin int64) int64 {
	usage, err := client.FetchUsage()
	if err != nil {
		if _, ok := err.(claude.ErrAuth); ok {
			log.Println("Token expired or revoked — run 'claude' to re-authenticate")
			sendLine(port, "E")
			return prevH5ResetMin
		}
		log.Printf("API error: %v", err)
		return prevH5ResetMin
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
	sendLine(port, msg)
	return h5resetMin
}

func sendLine(port serial.Port, s string) {
	port.Write([]byte(s + "\n"))
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
