package main

import (
	"image/color"
	"machine"
	"time"

	"tinygo.org/x/drivers"
)

const (
	epdW = 128
	epdH = 296
)

// redPixelClearsBit controls the red RAM polarity for the SSD1680.
// true = clearing a bit (0) marks a red pixel, 0xFF = no red.
// If red appears inverted on the display, flip this single constant.
const redPixelClearsBit = false

// useFastRefresh writes a custom LUT (cmd 0x32) with explicit voltage registers
// (0x03/0x04/0x2C/0x3F) and uses 0xC7 (no OTP reload) for display updates.
// This gives 4 visible transitions instead of ~15 with the OTP waveform.
// Set to false to use the standard OTP waveform (0xF7, slow but most reliable).
const useFastRefresh = true

// fullLUT: 4 groups — clear-to-black, clear-to-white, apply BW, apply Red.
// Used for initial display and when red content changes.
var fullLUT = [153]byte{
	// LUT0 (black, R=0 BW=0): G0=VSL G1=VSH1 G2=VSL G3=0
	0x80, 0x40, 0x80, 0x00, 0, 0, 0, 0, 0, 0, 0, 0,
	// LUT1 (white, R=0 BW=1): G0=VSL G1=VSH1 G2=VSH1 G3=0
	0x80, 0x40, 0x40, 0x00, 0, 0, 0, 0, 0, 0, 0, 0,
	// LUT2 (red, R=1 BW=0): G0=VSL G1=VSH1 G2=0 G3=VSH2
	0x80, 0x40, 0x00, 0xC0, 0, 0, 0, 0, 0, 0, 0, 0,
	// LUT3 (red, R=1 BW=1): same as LUT2
	0x80, 0x40, 0x00, 0xC0, 0, 0, 0, 0, 0, 0, 0, 0,
	// LUT4 (VCOM): all DCVCOM
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// Group 0 (clear to black): TP=10, RP=0
	10, 0, 0, 0, 0, 0, 0,
	// Group 1 (clear to white): TP=10, RP=0
	10, 0, 0, 0, 0, 0, 0,
	// Group 2 (BW content): TP=20, RP=1 (2 reps)
	20, 0, 0, 0, 0, 0, 1,
	// Group 3 (Red content): TP=40, RP=2 (3 reps)
	40, 0, 0, 0, 0, 0, 2,
	// Groups 4-11: inactive
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// FR
	0x33, 0x33, 0, 0, 0, 0,
	// XON
	0, 0, 0,
}

// diffLUT: 1 group — differential B/W transitions only, no clearing.
// In Mode 2, the SSD1680 compares old and new RAM; only changed pixels are driven.
// LUT0 = old0→new0 (same, no drive), LUT1 = 0→1 (VSH1→white),
// LUT2 = 1→0 (VSL→black), LUT3 = old1→new1 (same, no drive).
var diffLUT = [153]byte{
	// LUT0 (no change): all VSS
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// LUT1 (black→white): G0=VSH1
	0x40, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// LUT2 (white→black): G0=VSL
	0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// LUT3 (no change): all VSS
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// LUT4 (VCOM): all DCVCOM
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	// Group 0: TP=10, RP=1 (2 reps)
	10, 0, 0, 0, 0, 0, 1,
	// Groups 1-11: inactive
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0,
	// FR
	0x33, 0, 0, 0, 0, 0,
	// XON
	0, 0, 0,
}

type EPD struct {
	bus       drivers.SPI
	cs        machine.Pin
	dc        machine.Pin
	rst       machine.Pin
	busy      machine.Pin
	buffer    [epdW / 8 * epdH]uint8 // black/white RAM
	redBuffer [epdW / 8 * epdH]uint8 // red RAM

	DiffCount     int
	LastRefreshMS int
	LastTimeout   bool
}

func NewEPD(bus drivers.SPI, cs, dc, rst, busy machine.Pin) EPD {
	cs.Configure(machine.PinConfig{Mode: machine.PinOutput})
	dc.Configure(machine.PinConfig{Mode: machine.PinOutput})
	rst.Configure(machine.PinConfig{Mode: machine.PinOutput})
	busy.Configure(machine.PinConfig{Mode: machine.PinInput})
	cs.High()
	dc.High()
	rst.High()
	return EPD{bus: bus, cs: cs, dc: dc, rst: rst, busy: busy}
}

func (d *EPD) Configure() {
	d.hwReset()
	time.Sleep(200 * time.Millisecond)

	d.cmd(0x12) // software reset
	time.Sleep(200 * time.Millisecond)

	d.cmd(0x01) // driver output control
	d.data(0x27) // (296-1) & 0xFF
	d.data(0x01) // (296-1) >> 8
	d.data(0x00)

	d.cmd(0x11) // data entry mode
	d.data(0x03) // X inc, Y inc

	d.cmd(0x3C) // border waveform
	d.data(0x05)

	d.cmd(0x18) // temperature sensor
	d.data(0x80) // internal

	d.cmd(0x21) // display update control 1
	d.data(0x00) // A: normal BW + red RAM (no inversion)
	d.data(0x80) // B[7]=1: source output S8-S167 (matches 128px panel)

	d.setWindow(0, 0, epdW-1, epdH-1)
	d.setPointerNoWait(0, 0)
	time.Sleep(50 * time.Millisecond)

	for i := range d.buffer {
		d.buffer[i] = 0xFF
	}
	for i := range d.redBuffer {
		d.redBuffer[i] = 0x00
	}

	// No custom LUT written here — DisplayFull() uses OTP (0xF7),
	// DisplayDiff() writes diffLUT + voltage registers before each call.
}

func (d *EPD) SetPixel(x, y int16, c color.RGBA) {
	// Rotation: 270 degrees (landscape, 296x128)
	x, y = y, epdH-1-x
	if x < 0 || x >= epdW || y < 0 || y >= epdH {
		return
	}
	idx := int(x/8) + int(y)*(epdW/8)
	bit := uint8(0x80) >> uint8(x%8)

	isRed := c.R > 128 && c.G < 128 && c.B < 128

	if isRed {
		// Red pixel: BW = white (set bit), Red = mark red
		d.buffer[idx] |= bit
		if redPixelClearsBit {
			d.redBuffer[idx] &^= bit // clear bit = red
		} else {
			d.redBuffer[idx] |= bit // set bit = red
		}
	} else if c.R == 0 && c.G == 0 && c.B == 0 {
		// Black pixel: BW = black (clear bit), Red = no red
		d.buffer[idx] &^= bit
		if redPixelClearsBit {
			d.redBuffer[idx] |= bit // set bit = no red
		} else {
			d.redBuffer[idx] &^= bit // clear bit = no red
		}
	} else {
		// White pixel: BW = white (set bit), Red = no red
		d.buffer[idx] |= bit
		if redPixelClearsBit {
			d.redBuffer[idx] |= bit // set bit = no red
		} else {
			d.redBuffer[idx] &^= bit // clear bit = no red
		}
	}
}

// Display does a full refresh (used by error/setup/calibration screens).
func (d *EPD) Display() error {
	return d.DisplayFull()
}

// DisplayFull writes both BW and Red buffers and does a full OTP refresh with
// proper clearing phases. Always uses 0xF7 (load temp + OTP LUT + display) —
// the OTP waveform is the only reliable way to drive red and do a proper clear.
func (d *EPD) DisplayFull() error {
	d.setWindow(0, 0, epdW-1, epdH-1)
	d.setPointerNoWait(0, 0)
	time.Sleep(5 * time.Millisecond)

	d.cmd(0x24)
	d.dataBlock(d.buffer[:])

	d.setPointerNoWait(0, 0)
	d.cmd(0x26)
	d.dataBlock(d.redBuffer[:])

	// Always use OTP for full refresh — custom LUT voltage values are
	// panel-specific and ours aren't calibrated. OTP is reliable.
	d.cmd(0x22)
	d.data(0xF7) // load temp + load OTP LUT + display Mode 1
	d.cmd(0x20)

	ms, timedOut := d.waitBusy(25000)
	d.LastRefreshMS = ms
	d.LastTimeout = timedOut
	d.DiffCount = 0
	return nil
}

// DisplayDiff writes only the BW buffer and uses Mode 2 (differential) so the
// SSD1680 compares old and new RAM and only drives pixels that changed.
// Unchanged pixels don't flash at all. Red RAM is untouched — red stays visible.
func (d *EPD) DisplayDiff() error {
	if !useFastRefresh {
		return d.DisplayFull()
	}

	// Write voltage registers + custom diffLUT. The preceding DisplayFull()
	// used 0xF7 which loaded OTP values — we must overwrite with our own.
	d.cmd(0x03)
	d.data(0x17) // VGH = 20V
	d.cmd(0x04)
	d.data(0x41) // VSH1
	d.data(0xAE) // VSH2
	d.data(0x32) // VSL
	d.cmd(0x2C)
	d.data(0x36) // VCOM
	d.cmd(0x3F)
	d.data(0x22) // EOPT
	d.cmd(0x32)
	d.dataBlock(diffLUT[:])

	d.setWindow(0, 0, epdW-1, epdH-1)
	d.setPointerNoWait(0, 0)
	time.Sleep(5 * time.Millisecond)

	d.cmd(0x24)
	d.dataBlock(d.buffer[:])
	// Red RAM (0x26) intentionally NOT written — red pixels stay from last full refresh

	d.cmd(0x22)
	d.data(0xCF) // custom LUT + Mode 2 (differential)
	d.cmd(0x20)

	ms, timedOut := d.waitBusy(25000)
	d.LastRefreshMS = ms
	d.LastTimeout = timedOut
	d.DiffCount++
	return nil
}

func (d *EPD) ClearDisplay() {
	d.ClearBuffer()
	d.DisplayFull()
}

func (d *EPD) ClearBuffer() {
	for i := range d.buffer {
		d.buffer[i] = 0xFF
	}
	for i := range d.redBuffer {
		d.redBuffer[i] = 0x00 // no red
	}
}

// FillBlack fills the BW buffer with all black and red buffer with no-red.
func (d *EPD) FillBlack() {
	for i := range d.buffer {
		d.buffer[i] = 0x00 // all black
	}
	for i := range d.redBuffer {
		d.redBuffer[i] = 0x00 // no red
	}
}

func (d *EPD) Size() (int16, int16) {
	return epdH, epdW // rotated: 296x128
}

// BusyNow returns the current level of the BUSY pin (true=HIGH=busy).
func (d *EPD) BusyNow() bool {
	return d.busy.Get()
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

func (d *EPD) setPointer(x, y int16) {
	d.cmd(0x4E)
	d.data(uint8(x / 8))
	d.cmd(0x4F)
	d.data(uint8(y & 0xFF))
	d.data(uint8(y >> 8))
	d.waitBusy(3000)
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
