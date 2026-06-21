package main

import (
	"machine"
	"strconv"
	"strings"
	"time"
)

const buildID = "CLUE-FW-19"

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
	lastUsage    = UsageData{H5Limit: -1, H5ResetMin: -1, W1ResetDay: -1, W1ResetMin: -1}
	serialBuf    [4096]byte
	serialPos    int
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
			" tier=" + display.LastTier)

	case line == "E":
		currentState = stateError
		showError(&display)

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

func parseUsage(data string) (UsageData, bool) {
	parts := strings.Split(data, ":")
	if len(parts) < 7 {
		return UsageData{}, false
	}
	h5u, err1 := strconv.ParseInt(parts[0], 10, 64)
	h5l, err2 := strconv.ParseInt(parts[1], 10, 64)
	w1u, err3 := strconv.ParseInt(parts[2], 10, 64)
	w1l, err4 := strconv.ParseInt(parts[3], 10, 64)
	h5rm, err5 := strconv.ParseInt(parts[4], 10, 64)
	w1rd, err6 := strconv.ParseInt(parts[5], 10, 64)
	w1rm, err7 := strconv.ParseInt(parts[6], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || err6 != nil || err7 != nil {
		return UsageData{}, false
	}
	return UsageData{
		H5Used:     h5u,
		H5Limit:    h5l,
		W1Used:     w1u,
		W1Limit:    w1l,
		H5ResetMin: h5rm,
		W1ResetDay: w1rd,
		W1ResetMin: w1rm,
	}, true
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

