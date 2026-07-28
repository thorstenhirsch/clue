package main

import (
	"image/color"
	"machine"
	"strconv"
	"strings"
	"time"
)

const (
	buildID        = "CLUE-FW-27"
	hostLEDTimeout = 60 * time.Second
)

type state uint8

const (
	stateNoToken state = iota
	stateWaiting
	stateRunning
	stateError
)

var (
	display      EPD
	currentState state
	lastUsage    = UsageData{
		PrimaryLimit: -1, PrimaryResetDay: -1, PrimaryResetMin: -1,
		SecondaryResetDay: -1, SecondaryResetMin: -1,
	}
	serialBuf        [4096]byte
	serialPos        int
	led              machine.Pin
	hostActive       bool
	lastHostActivity time.Time

	refreshExperimentBWInverted bool
)

func main() {
	time.Sleep(2 * time.Second)

	machine.SPI0.Configure(machine.SPIConfig{
		Frequency: 4_000_000,
		SCK:       machine.P0_22,
		SDO:       machine.P0_24,
	})

	display = NewEPD(
		machine.SPI0,
		machine.P0_06, // CS
		machine.P0_08, // DC
		machine.P0_17, // RST
		machine.P0_20, // BUSY
	)
	display.Configure()

	led = machine.P0_11
	led.Configure(machine.PinConfig{Mode: machine.PinOutput})
	led.Low() // reading light stays off until a host daemon connects

	token := readToken()
	if token == "" {
		currentState = stateNoToken
	} else {
		currentState = stateWaiting
	}

	greeting := "N"
	if token != "" {
		greeting = "R"
	}

	heartbeatTicks := 0
	for {
		line := readLine()
		if line != "" {
			handleMessage(line)
		} else {
			time.Sleep(50 * time.Millisecond)
			if hostActive && time.Since(lastHostActivity) >= hostLEDTimeout {
				led.Low()
				hostActive = false
			}
			heartbeatTicks++
			if heartbeatTicks >= 40 {
				sendLine(greeting)
				heartbeatTicks = 0
			}
		}
	}
}

func handleMessage(line string) {
	if len(line) == 0 {
		return
	}
	noteHostActivity()
	switch {
	case line == "G":
		token := readToken()
		if token == "" {
			sendLine("N")
		} else {
			sendLine("T:" + token)
		}

	case strings.HasPrefix(line, "U:"):
		u, ok := parseUsage(line[2:])
		if !ok {
			return
		}
		currentState = stateRunning
		updateUsage(&display, &lastUsage, u)
		sendLine("DBG:U ms=" + strconv.Itoa(display.LastRefreshMS) +
			" diff=" + strconv.Itoa(display.DiffCount) +
			" ghost=" + strconv.Itoa(display.GhostBudget) +
			" tier=" + display.LastTier)

	case line == "E":
		currentState = stateError
		showError(&display)

	case strings.HasPrefix(line, "A:"):
		setProvider(line[2:])

	case strings.HasPrefix(line, "S:"):
		token := line[2:]
		if err := writeToken(token); err != nil {
			sendLine("F")
			return
		}
		sendLine("K")
		currentState = stateWaiting
		renderConnectingScreen(&display)
		display.Display()

	case line == "F":
		if currentState == stateRunning {
			renderUsageScreen(&display, &lastUsage)
			display.DisplayFull()
		}

	case line == "T:B":
		display.FillBlack()
		display.Display()
		sendLine("DBG:T:B done ms=" + strconv.Itoa(display.LastRefreshMS) +
			" timeout=" + strconv.FormatBool(display.LastTimeout))

	case line == "T:W":
		display.ClearBuffer()
		display.Display()
		sendLine("DBG:T:W done ms=" + strconv.Itoa(display.LastRefreshMS) +
			" timeout=" + strconv.FormatBool(display.LastTimeout))

	case line == "T:C":
		renderCalibration(&display)
		display.Display()
		sendLine("DBG:T:C done ms=" + strconv.Itoa(display.LastRefreshMS) +
			" timeout=" + strconv.FormatBool(display.LastTimeout))

	case strings.HasPrefix(line, "Q:"):
		handleRefreshExperiment(line[2:])

	case line == "L:0":
		led.Low()
		hostActive = false
	case line == "L:1":
		led.High()

	case line == "M:0":
		display.FastFullMode = false
		sendLine("DBG:M fastfull=false")
	case line == "M:1":
		display.FastFullMode = true
		sendLine("DBG:M fastfull=true")

	case strings.HasPrefix(line, "X:"):
		handleTuning(line[2:])

	case line == "P":
		if currentState == stateRunning {
			renderUsageScreen(&display, &lastUsage)
			display.RefreshSmart()
			sendLine("DBG:P ms=" + strconv.Itoa(display.LastRefreshMS) +
				" diff=" + strconv.Itoa(display.DiffCount) +
				" timeout=" + strconv.FormatBool(display.LastTimeout))
		}
	}
}

// handleRefreshExperiment serves the clue-test hardware lab. Q: commands are
// intentionally separate from the production protocol and settings are never
// persisted. Every BASE/RECOVER command uses the panel's true OTP waveform.
func handleRefreshExperiment(data string) {
	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		sendLine("DBG:Q parse error")
		return
	}
	test, stage := parts[0], parts[1]
	switch test + ":" + stage {
	case "BW:BASE":
		refreshExperimentBWInverted = false
		renderBWExperiment(&display, false)
		display.DisplayFull()
	case "BW:RUN":
		reps, frames, ok := experimentArgs(parts, 1, 50, 1, 255)
		if !ok {
			return
		}
		refreshExperimentBWInverted = !refreshExperimentBWInverted
		renderBWExperiment(&display, refreshExperimentBWInverted)
		if !display.refreshSelectiveBW(reps, frames, "test-bw") && display.LastTier == "test-rejected" {
			sendLine("DBG:Q rejected (red pixels present)")
			return
		}
	case "RA:BASE":
		renderRedAddExperiment(&display, false)
		display.DisplayFull()
	case "RA:RUN":
		passes, rp, ok := experimentArgs(parts, 1, 6, 0, 255)
		if !ok {
			return
		}
		renderRedAddExperiment(&display, true)
		if !display.refreshSelectiveRed(passes, rp) && display.LastTier == "test-rejected" {
			sendLine("DBG:Q rejected (red removal requested)")
			return
		}
	case "RC:BASE":
		renderRedClearExperiment(&display, false)
		display.DisplayFull()
	case "RC:RUN":
		passes, rp, ok := experimentArgs(parts, 1, 6, 0, 255)
		if !ok {
			return
		}
		renderRedClearExperiment(&display, true)
		display.refreshRedClear(passes, rp)
	case "ALL:RECOVER":
		renderRefreshReference(&display)
		display.DisplayFull()
	default:
		sendLine("DBG:Q unknown experiment")
		return
	}
	sendLine("DBG:Q test=" + test + " stage=" + stage +
		" fw=" + buildID +
		" ms=" + strconv.Itoa(display.LastRefreshMS) +
		" tier=" + display.LastTier +
		" timeout=" + strconv.FormatBool(display.LastTimeout))
}

func experimentArgs(parts []string, minA, maxA, minB, maxB int) (int, int, bool) {
	if len(parts) != 4 {
		sendLine("DBG:Q expected two numeric parameters")
		return 0, 0, false
	}
	a, e1 := strconv.Atoi(parts[2])
	b, e2 := strconv.Atoi(parts[3])
	if e1 != nil || e2 != nil || a < minA || a > maxA || b < minB || b > maxB {
		sendLine("DBG:Q invalid parameters")
		return 0, 0, false
	}
	return a, b, true
}

func fillTestRect(d *EPD, x, y, w, h int16, c color.RGBA) {
	for py := y; py < y+h; py++ {
		for px := x; px < x+w; px++ {
			d.SetPixel(px, py, c)
		}
	}
}

func renderRefreshReference(d *EPD) {
	d.ClearBuffer()
	fillTestRect(d, 8, 8, 84, 48, black)
	fillTestRect(d, 106, 8, 84, 48, red)
	// The right block stays white; the three large swatches make recovery and
	// relative pigment strength easy to judge without relying on tiny text.
	fillTestRect(d, 204, 8, 84, 48, white)
}

func renderBWExperiment(d *EPD, inverted bool) {
	d.ClearBuffer()
	for y := int16(8); y < 120; y += 16 {
		for x := int16(8); x < 288; x += 16 {
			dark := ((x-8)/16+(y-8)/16)%2 == 0
			if inverted {
				dark = !dark
			}
			if dark {
				fillTestRect(d, x, y, 16, 16, black)
			}
		}
	}
	// Solid rails expose weak black and residual white more clearly than the
	// checkerboard alone.
	if inverted {
		fillTestRect(d, 0, 0, 296, 5, black)
	} else {
		fillTestRect(d, 0, 123, 296, 5, black)
	}
}

func renderRedAddExperiment(d *EPD, add bool) {
	d.ClearBuffer()
	fillTestRect(d, 12, 16, 80, 80, black)
	fillTestRect(d, 108, 16, 80, 80, red) // existing red reference
	fillTestRect(d, 204, 16, 80, 80, black)
	if add {
		fillTestRect(d, 12, 16, 80, 80, red)  // black -> new red
		fillTestRect(d, 204, 16, 40, 80, red) // adjacent new red/black edge
	}
}

func renderRedClearExperiment(d *EPD, clear bool) {
	d.ClearBuffer()
	fillTestRect(d, 12, 16, 80, 80, red)
	fillTestRect(d, 108, 16, 80, 80, red)
	fillTestRect(d, 204, 16, 80, 80, red)
	if clear {
		fillTestRect(d, 12, 16, 80, 80, white)  // red -> white
		fillTestRect(d, 108, 16, 80, 80, black) // red -> black
		// Third swatch remains red as a saturation reference.
	}
}

func noteHostActivity() {
	lastHostActivity = time.Now()
	if !hostActive {
		led.High()
		hostActive = true
	}
}

func setProvider(id string) {
	switch id {
	case "claude":
		displayHeadline = "CLAUDE PRO"
		setupDescription = "Claude Usage E-Ink Display"
		reauthentication = "Run 'claude' to re-authenticate"
	case "codex":
		displayHeadline = "OPENAI CODEX"
		setupDescription = "Codex Usage E-Ink Display"
		reauthentication = "Run 'codex login' to re-auth"
	}
}

// handleTuning parses "X:<bwReps>:<triPasses>:<redRP>" and updates the
// live refresh parameters (RAM-only, reset to defaults on reboot).
// "X:6:3:3" = defaults. See the tunable vars in epd.go.
func handleTuning(data string) {
	parts := strings.Split(data, ":")
	if len(parts) < 3 {
		sendLine("DBG:X parse error")
		return
	}
	bw, err1 := strconv.Atoi(parts[0])
	passes, err2 := strconv.Atoi(parts[1])
	rp, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil ||
		bw < 1 || bw > 50 || passes < 1 || passes > 6 || rp < 0 || rp > 255 {
		sendLine("DBG:X invalid (bwReps 1-50, triPasses 1-6, redRP 0-255)")
		return
	}
	bwReps = bw
	triPasses = passes
	redRP = rp
	sendLine("DBG:X bwReps=" + strconv.Itoa(bwReps) +
		" triPasses=" + strconv.Itoa(triPasses) +
		" redRP=" + strconv.Itoa(redRP))
}

func parseUsage(data string) (UsageData, bool) {
	parts := strings.Split(data, ":")
	if len(parts) < 7 {
		return UsageData{}, false
	}
	primaryUsed, err1 := strconv.ParseInt(parts[0], 10, 64)
	primaryLimit, err2 := strconv.ParseInt(parts[1], 10, 64)
	secondaryUsed, err3 := strconv.ParseInt(parts[2], 10, 64)
	secondaryLimit, err4 := strconv.ParseInt(parts[3], 10, 64)
	primaryResetMin, err5 := strconv.ParseInt(parts[4], 10, 64)
	secondaryResetDay, err6 := strconv.ParseInt(parts[5], 10, 64)
	secondaryResetMin, err7 := strconv.ParseInt(parts[6], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || err6 != nil || err7 != nil {
		return UsageData{}, false
	}
	u := UsageData{
		PrimaryUsed: primaryUsed, PrimaryLimit: primaryLimit,
		SecondaryUsed: secondaryUsed, SecondaryLimit: secondaryLimit,
		PrimaryResetDay: -1, PrimaryResetMin: primaryResetMin,
		SecondaryResetDay: secondaryResetDay, SecondaryResetMin: secondaryResetMin,
		PrimaryWindowMin: 5 * 60, SecondaryWindowMin: 7 * 24 * 60,
	}
	if len(parts) >= 10 {
		primaryResetDay, err8 := strconv.ParseInt(parts[7], 10, 64)
		primaryWindowMin, err9 := strconv.ParseInt(parts[8], 10, 64)
		secondaryWindowMin, err10 := strconv.ParseInt(parts[9], 10, 64)
		if err8 != nil || err9 != nil || err10 != nil {
			return UsageData{}, false
		}
		u.PrimaryResetDay = primaryResetDay
		u.PrimaryWindowMin = primaryWindowMin
		u.SecondaryWindowMin = secondaryWindowMin
	}
	return u, true
}

func readLine() string {
	for {
		if machine.Serial.Buffered() == 0 {
			return ""
		}
		b, err := machine.Serial.ReadByte()
		if err != nil {
			return ""
		}
		if b == '\n' || b == '\r' {
			if serialPos == 0 {
				continue
			}
			line := string(serialBuf[:serialPos])
			serialPos = 0
			return line
		}
		if serialPos < len(serialBuf) {
			serialBuf[serialPos] = b
			serialPos++
		}
	}
}

func sendLine(s string) {
	machine.Serial.Write([]byte(s))
	machine.Serial.Write([]byte{'\n'})
}

// blink produces n dark pulses on the reading-light LED.
// The LED idles HIGH (on), so each cycle is off→on.
func blink(n int) {
	for i := 0; i < n; i++ {
		led.Low()
		time.Sleep(200 * time.Millisecond)
		led.High()
		time.Sleep(200 * time.Millisecond)
	}
}
