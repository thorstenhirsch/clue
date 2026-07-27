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
	cmdFlag := flag.String("cmd", "", "send a single raw command line (e.g. \"X:6:2:4\" or \"M:1\"), print responses, exit")
	providerFlag := flag.String("provider", "claude", "display provider branding: claude or codex")
	refreshTestFlag := flag.String("refresh-test", "", "guided waveform lab: bw, red-add, red-clear, cadence, or all")
	bwRepsFlag := flag.Int("bw-reps", 4, "selective B/W repetitions")
	bwFramesFlag := flag.Int("bw-frames", 10, "frames per selective B/W repetition")
	redPassesFlag := flag.Int("red-passes", 3, "red add/clear interleaved passes")
	redRPFlag := flag.Int("red-rp", 4, "red drive/clear repeat field (4 means 5 repetitions)")
	flag.Parse()
	if *providerFlag != "claude" && *providerFlag != "codex" {
		log.Fatalf("Invalid provider %q; use claude or codex", *providerFlag)
	}

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
	sendLine(port, "A:"+*providerFlag)

	if *cmdFlag != "" {
		log.Printf("-> %s", *cmdFlag)
		sendLine(port, *cmdFlag)
		if strings.HasPrefix(*cmdFlag, "Q:") {
			waitForQResponse(port, *cmdFlag, 35*time.Second, sigCh)
		} else {
			drainResponse(port, 3*time.Second)
		}
		return
	}
	if *refreshTestFlag != "" {
		cfg := refreshLabConfig{bwReps: *bwRepsFlag, bwFrames: *bwFramesFlag,
			redPasses: *redPassesFlag, redRP: *redRPFlag}
		runRefreshLab(port, *refreshTestFlag, cfg, sigCh)
		return
	}

	limit := 1_000_000

	steps := []step{
		// --- Sequence 1: full lifecycle ---
		// LED is on from boot. First reading suppresses blink even though
		// it's a "new" value — the sentinel PrimaryLimit=-1 guard prevents it.
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
				"870:3:870:3:300:10080"
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

type refreshLabConfig struct {
	bwReps, bwFrames, redPasses, redRP int
}

func runRefreshLab(port serial.Port, name string, cfg refreshLabConfig, sigCh <-chan os.Signal) {
	if name != "bw" && name != "red-add" && name != "red-clear" && name != "cadence" && name != "all" {
		log.Fatalf("Invalid --refresh-test %q; use bw, red-add, red-clear, cadence, or all", name)
	}
	if cfg.bwReps < 1 || cfg.bwReps > 50 || cfg.bwFrames < 1 || cfg.bwFrames > 255 ||
		cfg.redPasses < 1 || cfg.redPasses > 6 || cfg.redRP < 0 || cfg.redRP > 255 {
		log.Fatal("Refresh-test parameters out of range")
	}
	reader := bufio.NewReader(os.Stdin)
	log.Printf("Waveform lab: bw=%dx%d frames, red=%d passes RP=%d", cfg.bwReps,
		cfg.bwFrames, cfg.redPasses, cfg.redRP)
	log.Println("The firmware will always finish with a true OTP recovery refresh.")

	if name == "bw" || name == "all" {
		if !labStage(port, reader, sigCh, "B/W reference", "Q:BW:BASE", true,
			"Expect a crisp checkerboard with a solid black rail at the bottom.") ||
			!labStage(port, reader, sigCh, "Selective B/W inversion",
				fmt.Sprintf("Q:BW:RUN:%d:%d", cfg.bwReps, cfg.bwFrames), false,
				"Checkerboard must invert completely. Check black density, clean white squares, and old-edge ghosts.") {
			recoverRefreshLab(port)
			return
		}
	}
	if name == "red-add" || name == "all" {
		if !labStage(port, reader, sigCh, "Red-add OTP baseline", "Q:RA:BASE", true,
			"Three freshly painted blocks: black, red, black. Note the OTP red strength.") ||
			!labStage(port, reader, sigCh, "Selective red addition",
				fmt.Sprintf("Q:RA:RUN:%d:%d", cfg.redPasses, cfg.redRP), false,
				"Left block and half the right block should become strong red without black shadows; center is an undriven OTP-red reference.") {
			recoverRefreshLab(port)
			return
		}
	}
	if name == "red-clear" || name == "all" {
		if !labStage(port, reader, sigCh, "Red-clear reference", "Q:RC:BASE", true,
			"All three blocks should be fully saturated red.") ||
			!labStage(port, reader, sigCh, "EXPERIMENTAL red removal",
				fmt.Sprintf("Q:RC:RUN:%d:%d", cfg.redPasses, cfg.redRP), false,
				"Left must become clean white, center dense black, right remain red. Look closely for pink residue.") {
			recoverRefreshLab(port)
			return
		}
	}
	if name == "cadence" || name == "all" {
		if !labStage(port, reader, sigCh, "Cadence reference", "Q:BW:BASE", true,
			"Start from a crisp checkerboard. The next test alternates it 32 times without an intervening full refresh.") {
			recoverRefreshLab(port)
			return
		}
		for i := 1; i <= 32; i++ {
			command := fmt.Sprintf("Q:BW:RUN:%d:%d", cfg.bwReps, cfg.bwFrames)
			sendLine(port, command)
			if !waitForQResponse(port, command, 10*time.Second, sigCh) {
				recoverRefreshLab(port)
				return
			}
			if i == 8 || i == 16 || i == 32 {
				log.Printf("CADENCE CHECK after %d selective updates: inspect white residue, black fading, and edge ghosts.", i)
				log.Print("Press Enter to continue...")
				if !waitForEnter(reader, sigCh) {
					recoverRefreshLab(port)
					return
				}
			}
		}
	}

	recoverRefreshLab(port)
	log.Println("Recovery reference: black, red, and white swatches should all be clean and strong.")
	log.Println("=== REFRESH LAB COMPLETE ===")
}

func recoverRefreshLab(port serial.Port) {
	log.Println("Applying mandatory true-OTP recovery...")
	command := "Q:ALL:RECOVER"
	sendLine(port, command)
	waitForQResponse(port, command, 35*time.Second, nil)
}

func labStage(port serial.Port, reader *bufio.Reader, sigCh <-chan os.Signal, label, command string, full bool, check string) bool {
	log.Printf("=== %s ===", label)
	log.Printf("-> %s", command)
	sendLine(port, command)
	wait := 10 * time.Second
	if full {
		wait = 35 * time.Second
	}
	if !waitForQResponse(port, command, wait, sigCh) {
		return false
	}
	log.Printf("VISUAL CHECK: %s", check)
	log.Print("Press Enter to continue (Ctrl-C aborts; rerun the lab to recover)...")
	return waitForEnter(reader, sigCh)
}

func waitForEnter(reader *bufio.Reader, sigCh <-chan os.Signal) bool {
	done := make(chan struct{}, 1)
	go func() {
		_, _ = reader.ReadString('\n')
		done <- struct{}{}
	}()
	select {
	case <-done:
		return true
	case sig := <-sigCh:
		log.Printf("Received %s; recovering the panel before exit", sig)
		return false
	}
}

func waitForQResponse(port serial.Port, command string, wait time.Duration, sigCh <-chan os.Signal) bool {
	var buf [512]byte
	pending := ""
	deadline := time.Now().Add(wait)
	interrupted := false
	expected := expectedQResponse(command)
	for time.Now().Before(deadline) {
		select {
		case sig := <-sigCh:
			if !interrupted {
				log.Printf("Received %s; waiting for the active refresh to finish, then recovering", sig)
				interrupted = true
			}
		default:
		}
		n, _ := port.Read(buf[:])
		if n == 0 {
			continue
		}
		pending += string(buf[:n])
		for {
			i := strings.IndexAny(pending, "\r\n")
			if i < 0 {
				break
			}
			line := strings.TrimSpace(pending[:i])
			pending = strings.TrimLeft(pending[i+1:], "\r\n")
			if line == "" {
				continue
			}
			if line == "N" || line == "R" {
				continue // suppress periodic firmware heartbeat noise
			}
			log.Printf("  <- %s", line)
			if strings.HasPrefix(line, expected) {
				return !interrupted
			}
			if strings.HasPrefix(line, "DBG:Q") &&
				(strings.Contains(line, "error") || strings.Contains(line, "invalid") ||
					strings.Contains(line, "unknown") || strings.Contains(line, "rejected")) {
				return false
			}
		}
	}
	log.Printf("Timed out waiting for experiment response after %s", wait)
	return false
}

func expectedQResponse(command string) string {
	parts := strings.Split(command, ":")
	if len(parts) >= 3 && parts[0] == "Q" {
		return "DBG:Q test=" + parts[1] + " stage=" + parts[2]
	}
	return "DBG:Q"
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
