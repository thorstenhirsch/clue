package main

import (
	"image/color"
	"machine"
	"time"

	"tinygo.org/x/drivers"
)

const (
	epdW      = 128
	epdH      = 296
	epdStride = epdW / 8         // 16 bytes per gate line
	epdBufSz  = epdStride * epdH // 4736 bytes
)

// redPixelClearsBit controls the red RAM polarity for the SSD1680.
// true = clearing a bit (0) marks a red pixel, 0xFF = no red.
// If red appears inverted on the display, flip this single constant.
const redPixelClearsBit = false

// Tunable refresh parameters (RAM-only; adjust live via the "X:" serial
// command, e.g. "X:6:3:3" = defaults). Hardware-tuned July 2026: bwReps 6 is
// the verified minimum with no shadows (X:6:2:4 showed shadows, so triPasses
// stays at 3). "X:10:3:4" reproduces the CLUE-FW-19 frame sequence.
var (
	bwReps    = 6 // B/W refresh: reps of the 10-frame reinforcement group
	triPasses = 3 // tri-color: number of interleaved [BW + red] group pairs
	redRP     = 3 // tri-color: RP field (3 = 4 reps × 30 frames), verified strong
)

const (
	selectiveBWReps   = 3
	selectiveBWFrames = 10
	ghostBudgetLimit  = 32
	fullDriveCost     = 4 // preserves full-after-8 for tri/fallback refreshes
)

// buildTriLUT constructs the no-clear tri-color LUT (cmd 0x32 + 0xC7, Mode 1).
// Directly applies target voltages without clearing phases, so unchanged
// pixels don't flash. The old 3-trigger pass loop lives inside the LUT as
// interleaved groups: pass p = group 2p (10-frame BW reinforce/clear) +
// group 2p+1 (30-frame VSH2 red drive × (redRP+1) reps). One activation
// instead of `passes` power-up/power-down cycles. Max 6 passes (12 groups).
// OTP-loaded voltages from the initial 0xF7 persist and are reused.
func buildTriLUT(passes, redRP int) [153]byte {
	var lut [153]byte
	for p := 0; p < passes; p++ {
		gBW := 2 * p
		gRed := gBW + 1
		// VS section: LUT n group g at byte n*12+g, phase A in D7-D6.
		lut[0*12+gBW] = 0x40  // LUT0 black: VSH1 (drives black on this panel)
		lut[1*12+gBW] = 0x80  // LUT1 white: VSL (drives white on this panel)
		lut[2*12+gBW] = 0x80  // LUT2 red: VSL clears BW residue to white
		lut[3*12+gBW] = 0x80  // LUT3 red: VSL clears BW residue to white
		lut[2*12+gRed] = 0xC0 // LUT2 red: VSH2 drives red pigment
		lut[3*12+gRed] = 0xC0 // LUT3 red: VSH2 drives red pigment
		// LUT4 (VCOM): stays VSS. Timing: 7 bytes per group from byte 60,
		// TP[A] first, RP last.
		lut[60+7*gBW] = 10
		lut[60+7*gRed] = 30
		lut[60+7*gRed+6] = byte(redRP)
	}
	// FR=3 for all groups (nibble-packed)
	for i := 144; i < 150; i++ {
		lut[i] = 0x33
	}
	return lut
}

// buildDiffLUT constructs the fast B/W LUT (cmd 0x32 + 0xC7, Mode 1): one
// group of 10 frames × reps. All pixels are driven to their target state on
// every refresh, preventing fading of unchanged pixels. On this panel VSH1
// drives black, VSL drives white (reversed from datasheet naming — confirmed
// by OTP behaviour). No voltage register writes needed — OTP-loaded voltages
// persist.
func buildDiffLUT(reps int) [153]byte {
	var lut [153]byte
	lut[0*12] = 0x40 // LUT0 (R=0 BW=0, black): reinforce with VSH1
	lut[1*12] = 0x80 // LUT1 (R=0 BW=1, white): reinforce with VSL
	lut[2*12] = 0x40 // LUT2 (unused — no red pixels in B/W-only refresh)
	lut[3*12] = 0x80 // LUT3 (unused — no red pixels in B/W-only refresh)
	// LUT4 (VCOM): stays VSS
	lut[60] = 10               // group 0: TP[A] = 10 frames
	lut[60+6] = byte(reps - 1) // RP = reps-1
	for i := 144; i < 150; i++ {
		lut[i] = 0x33
	}
	return lut
}

// box represents a dirty rectangle in buffer coordinates.
// bx0..bx1 = source byte columns (0–15), gy0..gy1 = gate rows (0–295).
type box struct {
	bx0, bx1 int16
	gy0, gy1 int16
	empty    bool
}

// diffBox scans two buffers and returns the tightest bounding box around all
// bytes that differ. Returns box{empty: true} if the buffers are identical.
func diffBox(a, b []byte) box {
	r := box{empty: true, bx0: epdStride - 1, gy0: epdH - 1}
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] {
			gy := int16(i / epdStride)
			bx := int16(i % epdStride)
			if r.empty {
				r.empty = false
				r.bx0, r.bx1 = bx, bx
				r.gy0, r.gy1 = gy, gy
			} else {
				if bx < r.bx0 {
					r.bx0 = bx
				}
				if bx > r.bx1 {
					r.bx1 = bx
				}
				if gy < r.gy0 {
					r.gy0 = gy
				}
				if gy > r.gy1 {
					r.gy1 = gy
				}
			}
		}
	}
	return r
}

// anyRedCleared returns true if any red pixel that was displayed has been
// cleared in the current buffer. With redPixelClearsBit=false, a set bit = red;
// "cleared" means a bit was 1 in disp but 0 in cur.
func anyRedCleared(disp, cur []byte) bool {
	for i := range disp {
		if disp[i]&^cur[i] != 0 {
			return true
		}
	}
	return false
}

func anyRedPixels(buf []byte) bool {
	for _, b := range buf {
		if b != 0 {
			return true
		}
	}
	return false
}

type EPD struct {
	bus  drivers.SPI
	cs   machine.Pin
	dc   machine.Pin
	rst  machine.Pin
	busy machine.Pin

	buffer    [epdBufSz]uint8 // working B/W RAM (render target)
	redBuffer [epdBufSz]uint8 // working red RAM (render target)

	// Snapshot of what's actually on the display, used to compute pixel diffs.
	dispBuffer    [epdBufSz]uint8
	dispRedBuffer [epdBufSz]uint8

	// Test-only encoded RAM planes. Experimental Mode-1 refreshes use the two
	// controller RAMs as four waveform selectors instead of as literal BW/red
	// images. Keeping these separate ensures the render buffers and displayed
	// snapshots always retain their normal meaning.
	testBW  [epdBufSz]uint8
	testRed [epdBufSz]uint8

	DiffCount     int // custom refreshes since last full (telemetry)
	GhostBudget   int // selective BW costs 1; tri/fallback costs 4; full at 32
	LastRefreshMS int
	LastTimeout   bool
	LastTier      string // "full", "fastfull", "tri", "bw", "skip" — last refresh method
	ForceFullNext bool   // set true to force full OTP on next RefreshSmart

	// FastFullMode (default true; "M:0" disables): RefreshSmart's internal
	// full refreshes (weighted anti-ghost budget, red-cleared) use temp-spoofed
	// 90°C OTP waveform instead of the sensor-temp 0xF7 — same phase
	// structure, faster clocking. Verified equivalent on hardware July 2026.
	// Explicit fulls (init and the host's 4am F) use true 0xF7.
	FastFullMode bool
}

func NewEPD(bus drivers.SPI, cs, dc, rst, busy machine.Pin) EPD {
	cs.Configure(machine.PinConfig{Mode: machine.PinOutput})
	dc.Configure(machine.PinConfig{Mode: machine.PinOutput})
	rst.Configure(machine.PinConfig{Mode: machine.PinOutput})
	busy.Configure(machine.PinConfig{Mode: machine.PinInput})
	cs.High()
	dc.High()
	rst.High()
	return EPD{bus: bus, cs: cs, dc: dc, rst: rst, busy: busy, FastFullMode: true}
}

func (d *EPD) Configure() {
	d.hwReset()
	time.Sleep(200 * time.Millisecond)

	d.cmd(0x12) // software reset
	time.Sleep(200 * time.Millisecond)

	d.initRegisters()

	for i := range d.buffer {
		d.buffer[i] = 0xFF
	}
	for i := range d.redBuffer {
		d.redBuffer[i] = 0x00
	}

	// Force full OTP refresh on first display to establish correct voltages
	// and initialize the dispBuffer/dispRedBuffer baselines.
	d.ForceFullNext = true
}

func (d *EPD) SetPixel(x, y int16, c color.RGBA) {
	// Rotation: 270 degrees (landscape, 296x128)
	x, y = y, epdH-1-x
	if x < 0 || x >= epdW || y < 0 || y >= epdH {
		return
	}
	idx := int(x/8) + int(y)*epdStride
	bit := uint8(0x80) >> uint8(x%8)

	isRed := c.R > 128 && c.G < 128 && c.B < 128

	if isRed {
		d.buffer[idx] |= bit
		if redPixelClearsBit {
			d.redBuffer[idx] &^= bit
		} else {
			d.redBuffer[idx] |= bit
		}
	} else if c.R == 0 && c.G == 0 && c.B == 0 {
		d.buffer[idx] &^= bit
		if redPixelClearsBit {
			d.redBuffer[idx] |= bit
		} else {
			d.redBuffer[idx] &^= bit
		}
	} else {
		d.buffer[idx] |= bit
		if redPixelClearsBit {
			d.redBuffer[idx] |= bit
		} else {
			d.redBuffer[idx] &^= bit
		}
	}
}

// Display does a full refresh (used by error/setup/calibration screens).
func (d *EPD) Display() error {
	return d.DisplayFull()
}

// DisplayFull writes both BW and Red buffers and does a full OTP refresh.
// Always uses 0xF7 which loads OTP LUT AND populates VGH/VSH/VSL/VCOM
// voltage registers from OTP — the voltages then persist for subsequent
// partial refreshes that use custom LUTs. Never write 0x03/0x04/0x2C/0x3F
// manually — that broke Attempts 2-4 by overriding the correct OTP values.
func (d *EPD) DisplayFull() error {
	return d.displayFull(false)
}

// displayFull runs a full OTP refresh. fast=false: 0xF7 (sensor temp, the
// reference waveform). fast=true (Waveshare V4 "fast" sequence): spoof 90°C
// via 0x1A, load the high-temp OTP band with 0x91, display with 0xC7 — same
// phase structure, shorter phases. initRegisters() afterwards restores the
// internal temp sensor (0x18=0x80), so the next 0xF7 uses the real band.
func (d *EPD) displayFull(fast bool) error {
	d.wake()
	d.setWindow(0, 0, epdW-1, epdH-1)
	d.setPointerNoWait(0, 0)
	time.Sleep(5 * time.Millisecond)

	d.cmd(0x24)
	d.dataBlock(d.buffer[:])

	d.setPointerNoWait(0, 0)
	d.cmd(0x26)
	d.dataBlock(d.redBuffer[:])

	totalMS := 0
	if fast {
		d.cmd(0x1A) // write temperature register: 0x5A0 = 90°C
		d.data(0x5A)
		d.data(0x00)
		d.cmd(0x22)
		d.data(0x91) // load OTP LUT using register temp (no display)
		d.cmd(0x20)
		ms, _ := d.waitBusy(5000)
		totalMS += ms
		d.cmd(0x22)
		d.data(0xC7) // display with the just-loaded high-temp OTP LUT
	} else {
		d.cmd(0x22)
		d.data(0xF7) // load temp + load OTP LUT + display Mode 1
	}
	d.cmd(0x20)

	ms, timedOut := d.waitBusy(25000)
	totalMS += ms

	// After the refresh the controller is in standby. Wake it, then
	// re-establish all control registers that the OTP may have modified.
	d.wake()
	d.initRegisters()

	d.LastRefreshMS = totalMS
	d.LastTimeout = timedOut
	d.DiffCount = 0
	d.GhostBudget = 0
	d.ForceFullNext = timedOut
	if fast {
		d.LastTier = "fastfull"
	} else {
		d.LastTier = "full"
	}

	if timedOut {
		return nil
	}

	// Snapshot the displayed image for future diffs only after BUSY completed.
	copy(d.dispBuffer[:], d.buffer[:])
	copy(d.dispRedBuffer[:], d.redBuffer[:])
	return nil
}

// RefreshSmart compares the working buffers against the last-displayed
// snapshot and picks the cheapest refresh tier:
//   - nothing changed → skip
//   - forced full / anti-ghost / red cleared → full-screen OTP (0xF7)
//   - red pixels added → tri-color custom LUT (0xC7, no-clear, additive)
//   - B/W only changed, no red present → fast selective update
//   - B/W only changed, red present → tri-color update (preserves red)
func (d *EPD) RefreshSmart() error {
	bwBox := diffBox(d.buffer[:], d.dispBuffer[:])
	redBox := diffBox(d.redBuffer[:], d.dispRedBuffer[:])

	if bwBox.empty && redBox.empty {
		d.LastTier = "skip"
		return nil // nothing changed
	}

	// Red pixels were removed (e.g. usage dropped below 80% after a reset) —
	// a no-clear tri-color pass can't erase red, so force a full OTP refresh.
	// FastFullMode applies only to the internal triggers (anti-ghost, red
	// cleared); ForceFullNext (init or timeout recovery) stays true 0xF7.
	if d.ForceFullNext || d.GhostBudget >= ghostBudgetLimit ||
		anyRedCleared(d.dispRedBuffer[:], d.redBuffer[:]) {
		return d.displayFull(d.FastFullMode && !d.ForceFullNext)
	}

	// Mode 1 selects a waveform from both RAM bits. The B/W reinforcement LUT's
	// red selector entries drive black/white, so using it while physical red is
	// present progressively erases that pigment even when the red buffer itself
	// is unchanged. Re-run the validated tri-color waveform to preserve and
	// reinforce red during unrelated B/W changes.
	if !redBox.empty || anyRedPixels(d.redBuffer[:]) {
		return d.refreshTriColor()
	}

	// With no physical red, use transition-selective Mode 1, verified clean
	// through 32 full-screen alternations.
	if d.refreshSelectiveBW(selectiveBWReps, selectiveBWFrames, "bw-selective") {
		return nil
	}
	return d.refreshPartialBW()
}

// refreshTriColor does a fast tri-color refresh using a custom no-clear LUT.
// Writes full BW+red buffers and triggers buildTriLUT + 0xC7 (custom LUT,
// Mode 1) once. No clearing phases — each pixel is directly driven to its
// target voltage, so unchanged pixels don't flash. Red pigment saturation is
// built up by the interleaved red groups inside the LUT (triPasses × redRP —
// the multi-trigger loop of CLUE-FW-19 folded into one activation, saving
// two power-up/power-down cycles). The OTP-loaded voltage registers persist
// from the initial 0xF7 and are reused.
func (d *EPD) refreshTriColor() error {
	d.wake()
	d.setWindow(0, 0, epdW-1, epdH-1)
	d.setPointerNoWait(0, 0)
	time.Sleep(5 * time.Millisecond)
	d.cmd(0x24)
	d.dataBlock(d.buffer[:])

	d.setPointerNoWait(0, 0)
	d.cmd(0x26)
	d.dataBlock(d.redBuffer[:])

	lut := buildTriLUT(triPasses, redRP)
	d.cmd(0x32)
	d.dataBlock(lut[:])

	d.cmd(0x22)
	d.data(0xC7) // custom LUT + Mode 1 + display + power down
	d.cmd(0x20)

	ms, timedOut := d.waitBusy(25000)

	d.LastRefreshMS = ms
	d.LastTimeout = timedOut
	if timedOut {
		d.ForceFullNext = true
		return nil
	}
	d.DiffCount++
	d.GhostBudget += fullDriveCost
	d.LastTier = "tri"

	copy(d.dispBuffer[:], d.buffer[:])
	copy(d.dispRedBuffer[:], d.redBuffer[:])
	return nil
}

// refreshPartialBW does a full-screen Mode 1 refresh for B/W-only changes.
// Uses buildDiffLUT + 0xC7 (custom LUT, Mode 1) — the same mode as OTP and
// refreshTriColor, avoiding Mode 2's controller-state sensitivity that
// caused B/W inversion after OTP refreshes. Writes the NEW B/W frame to
// 0x24 and the red buffer (all zeros for <80% usage) to 0x26. In Mode 1,
// LUT0 (R=0,BW=0)→VSH1→black and LUT1 (R=0,BW=1)→VSL→white (this panel's
// polarity). All pixels are driven to their target state bwReps times in a
// single activation (reinforcement prevents fading) — the 5-trigger loop of
// CLUE-FW-19 folded into the LUT's repeat count, saving four power cycles.
func (d *EPD) refreshPartialBW() error {
	// Defensive guard: buildDiffLUT cannot preserve red selector classes.
	// RefreshSmart normally routes this case before reaching this function.
	if anyRedPixels(d.dispRedBuffer[:]) || anyRedPixels(d.redBuffer[:]) {
		return d.refreshTriColor()
	}

	d.wake()
	d.setWindow(0, 0, epdW-1, epdH-1)

	d.setPointerNoWait(0, 0)
	time.Sleep(5 * time.Millisecond)
	d.cmd(0x24)
	d.dataBlock(d.buffer[:])

	d.setPointerNoWait(0, 0)
	d.cmd(0x26)
	d.dataBlock(d.redBuffer[:])

	lut := buildDiffLUT(bwReps)
	d.cmd(0x32)
	d.dataBlock(lut[:])

	d.cmd(0x22)
	d.data(0xC7) // custom LUT + Mode 1 + display + power down
	d.cmd(0x20)

	ms, timedOut := d.waitBusy(25000)

	d.LastRefreshMS = ms
	d.LastTimeout = timedOut
	if timedOut {
		d.ForceFullNext = true
		return nil
	}
	d.DiffCount++
	d.GhostBudget += fullDriveCost
	d.LastTier = "bw"

	copy(d.dispBuffer[:], d.buffer[:])
	copy(d.dispRedBuffer[:], d.redBuffer[:])
	return nil
}

// buildSelectiveBWLUT maps Mode-1 LUT1 to white->black and LUT2 to
// black->white. LUT0 is used for every unchanged pixel and remains VSS.
func buildSelectiveBWLUT(reps, frames int) [153]byte {
	var lut [153]byte
	lut[1*12] = 0x40 // W->B: VSH1 (black on this panel)
	lut[2*12] = 0x80 // B->W: VSL (white on this panel)
	lut[60] = byte(frames)
	lut[60+6] = byte(reps - 1)
	for i := 144; i < 150; i++ {
		lut[i] = 0x33
	}
	return lut
}

// refreshSelectiveBW obtains transition-aware behavior without SSD1680 Mode 2:
// the two Mode-1 RAM bits encode unchanged, W->B, and B->W. It is only valid
// while no physical red is displayed.
func (d *EPD) refreshSelectiveBW(reps, frames int, tier string) bool {
	for i := range d.dispRedBuffer {
		if d.dispRedBuffer[i] != 0 || d.redBuffer[i] != 0 {
			d.LastTier = "test-rejected"
			return false
		}
		d.testBW[i] = 0
		d.testRed[i] = 0
		old := d.dispBuffer[i]
		cur := d.buffer[i]
		// class 1 (red=0,bw=1): white -> black
		d.testBW[i] = old &^ cur
		// class 2 (red=1,bw=0): black -> white
		d.testRed[i] = (^old) & cur
	}
	lut := buildSelectiveBWLUT(reps, frames)
	d.refreshEncoded(d.testBW[:], d.testRed[:], lut[:], tier)
	return !d.LastTimeout
}

// buildSelectiveRedLUT maps class 0=existing red (VSS), class 1=new red,
// class 2=target black, class 3=target white. New red uses the same verified
// interleaved BW-clear/red-drive sequence as the production tri-color LUT.
func buildSelectiveRedLUT(passes, redRepeat int) [153]byte {
	var lut [153]byte
	for p := 0; p < passes; p++ {
		gBW, gRed := 2*p, 2*p+1
		lut[1*12+gBW] = 0x80  // new red: clear black residue
		lut[1*12+gRed] = 0xC0 // new red: drive red
		lut[2*12+gBW] = 0x40  // target black
		lut[3*12+gBW] = 0x80  // target white
		lut[60+7*gBW] = 10
		lut[60+7*gRed] = 30
		lut[60+7*gRed+6] = byte(redRepeat)
	}
	for i := 144; i < 150; i++ {
		lut[i] = 0x33
	}
	return lut
}

func (d *EPD) refreshSelectiveRed(passes, redRepeat int) bool {
	for i := range d.dispRedBuffer {
		if d.dispRedBuffer[i]&^d.redBuffer[i] != 0 {
			d.LastTier = "test-rejected"
			return false // this waveform is additive only
		}
		d.testBW[i], d.testRed[i] = 0, 0
		for bit := uint8(0x80); bit != 0; bit >>= 1 {
			class := uint8(0)
			oldRed := d.dispRedBuffer[i]&bit != 0
			newRed := d.redBuffer[i]&bit != 0
			switch {
			case oldRed && newRed:
				class = 0
			case newRed:
				class = 1
			case d.buffer[i]&bit == 0:
				class = 2
			default:
				class = 3
			}
			if class&1 != 0 {
				d.testBW[i] |= bit
			}
			if class&2 != 0 {
				d.testRed[i] |= bit
			}
		}
	}
	lut := buildSelectiveRedLUT(passes, redRepeat)
	d.refreshEncoded(d.testBW[:], d.testRed[:], lut[:], "test-red-add")
	return !d.LastTimeout
}

// buildRedClearLUT is intentionally experimental. Class 2 clears red to
// white; class 3 clears red and then drives black. Non-red pixels are VSS.
func buildRedClearLUT(passes, clearRepeat int) [153]byte {
	var lut [153]byte
	for p := 0; p < passes; p++ {
		gClear, gBlack := 2*p, 2*p+1
		lut[2*12+gClear] = 0x80
		lut[3*12+gClear] = 0x80
		lut[3*12+gBlack] = 0x40
		lut[60+7*gClear] = 30
		lut[60+7*gClear+6] = byte(clearRepeat)
		lut[60+7*gBlack] = 10
	}
	for i := 144; i < 150; i++ {
		lut[i] = 0x33
	}
	return lut
}

func (d *EPD) refreshRedClear(passes, clearRepeat int) bool {
	for i := range d.dispRedBuffer {
		d.testBW[i], d.testRed[i] = 0, 0
		for bit := uint8(0x80); bit != 0; bit >>= 1 {
			if d.dispRedBuffer[i]&bit == 0 {
				continue
			}
			class := uint8(2) // red -> white
			if d.buffer[i]&bit == 0 {
				class = 3
			} // red -> black
			if class&1 != 0 {
				d.testBW[i] |= bit
			}
			if class&2 != 0 {
				d.testRed[i] |= bit
			}
		}
	}
	lut := buildRedClearLUT(passes, clearRepeat)
	d.refreshEncoded(d.testBW[:], d.testRed[:], lut[:], "test-red-clear")
	return !d.LastTimeout
}

func (d *EPD) refreshEncoded(bw, red, lut []byte, tier string) {
	d.wake()
	d.setWindow(0, 0, epdW-1, epdH-1)
	d.setPointerNoWait(0, 0)
	time.Sleep(5 * time.Millisecond)
	d.cmd(0x24)
	d.dataBlock(bw)
	d.setPointerNoWait(0, 0)
	d.cmd(0x26)
	d.dataBlock(red)
	d.cmd(0x32)
	d.dataBlock(lut)
	d.cmd(0x22)
	d.data(0xC7)
	d.cmd(0x20)
	ms, timedOut := d.waitBusy(25000)
	d.LastRefreshMS, d.LastTimeout, d.LastTier = ms, timedOut, tier
	if timedOut {
		d.ForceFullNext = true
		return
	}
	d.DiffCount++
	if tier == "bw-selective" {
		d.GhostBudget++
	}
	copy(d.dispBuffer[:], d.buffer[:])
	copy(d.dispRedBuffer[:], d.redBuffer[:])
}

func (d *EPD) ClearBuffer() {
	for i := range d.buffer {
		d.buffer[i] = 0xFF
	}
	for i := range d.redBuffer {
		d.redBuffer[i] = 0x00
	}
}

// FillBlack fills the BW buffer with all black and red buffer with no-red.
func (d *EPD) FillBlack() {
	for i := range d.buffer {
		d.buffer[i] = 0x00
	}
	for i := range d.redBuffer {
		d.redBuffer[i] = 0x00
	}
}

func (d *EPD) Size() (int16, int16) {
	return epdH, epdW // rotated: 296x128
}

// wake brings the controller out of standby. Every refresh sequence
// (0xF7/0xC7) ends by disabling clock+analog, leaving the SSD1680 in
// standby where register and RAM writes are silently ignored. This must be
// called before any SPI writes that follow a completed refresh.
func (d *EPD) wake() {
	d.cmd(0x22)
	d.data(0xC0) // enable clock + analog
	d.cmd(0x20)
	d.waitBusy(1000)
}

// initRegisters sets all non-volatile control registers to known values.
// Called by Configure() at boot and by DisplayFull() after each OTP refresh,
// since 0xF7 may modify registers beyond cmd 0x21. Voltage registers
// (0x03/0x04/0x2C) are NOT touched — they persist from the OTP load.
func (d *EPD) initRegisters() {
	d.cmd(0x01)  // driver output control
	d.data(0x27) // (296-1) & 0xFF
	d.data(0x01) // (296-1) >> 8
	d.data(0x00)

	d.cmd(0x11)  // data entry mode
	d.data(0x03) // X inc, Y inc

	d.cmd(0x3C) // border waveform
	d.data(0x05)

	d.cmd(0x18)  // temperature sensor
	d.data(0x80) // internal

	d.cmd(0x21)  // display update control 1
	d.data(0x00) // A: normal BW + red RAM (no inversion)
	d.data(0x80) // B[7]=1: source output S8-S167 (matches 128px panel)

	d.setWindow(0, 0, epdW-1, epdH-1)
	d.setPointerNoWait(0, 0)
	time.Sleep(50 * time.Millisecond)
}

func (d *EPD) setWindow(xStart, yStart, xEnd, yEnd int16) {
	d.cmd(0x44)
	d.data(uint8(xStart / 8))
	d.data(uint8(xEnd / 8))
	d.cmd(0x45)
	d.data(uint8(yStart & 0xFF))
	d.data(uint8(yStart >> 8))
	d.data(uint8(yEnd & 0xFF))
	d.data(uint8(yEnd >> 8))
}

func (d *EPD) setPointerNoWait(x, y int16) {
	d.cmd(0x4E)
	d.data(uint8(x / 8))
	d.cmd(0x4F)
	d.data(uint8(y & 0xFF))
	d.data(uint8(y >> 8))
}

func (d *EPD) hwReset() {
	d.rst.High()
	time.Sleep(20 * time.Millisecond)
	d.rst.Low()
	time.Sleep(2 * time.Millisecond)
	d.rst.High()
	time.Sleep(20 * time.Millisecond)
}

// waitBusy waits for BUSY to go LOW (idle). SSD1680: HIGH = busy, LOW = ready.
// Returns elapsed ms and whether it timed out. Never blocks forever.
func (d *EPD) waitBusy(maxMS int) (int, bool) {
	time.Sleep(10 * time.Millisecond)
	ms := 10
	for d.busy.Get() {
		time.Sleep(10 * time.Millisecond)
		ms += 10
		if ms >= maxMS {
			return ms, true
		}
	}
	return ms, false
}

func (d *EPD) cmd(c uint8) {
	d.dc.Low()
	d.cs.Low()
	d.bus.Transfer(c)
	d.cs.High()
}

func (d *EPD) data(b uint8) {
	d.dc.High()
	d.cs.Low()
	d.bus.Transfer(b)
	d.cs.High()
}

// dataBlock sends a contiguous buffer as SPI data using a single bulk
// DMA transfer instead of per-byte Transfer() calls. This avoids the
// nRF52840 SPIM/USBD EasyDMA conflict that hangs per-byte loops.
func (d *EPD) dataBlock(buf []byte) {
	d.dc.High()
	d.cs.Low()
	d.bus.Tx(buf, nil)
	d.cs.High()
}
