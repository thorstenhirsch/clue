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

// useFastRefresh spoofs the temperature register to 90°C before each display
// update, causing the OTP waveform search to select WS7 (the high-temperature
// waveform with shorter/faster phases). Set to false to use the standard
// room-temperature waveform (slower but most reliable).
const useFastRefresh = true

type EPD struct {
	bus       drivers.SPI
	cs        machine.Pin
	dc        machine.Pin
	rst       machine.Pin
	busy      machine.Pin
	buffer    [epdW / 8 * epdH]uint8 // black/white RAM
	redBuffer [epdW / 8 * epdH]uint8 // red RAM

	// Set by Display() for diagnostic reporting.
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

	if useFastRefresh {
		// Temperature trick: load OTP with spoofed 90°C so the OTP search
		// selects WS7 (high-temp waveform with shorter/faster phases).
		d.cmd(0x22)
		d.data(0xB1) // load temp from sensor + load LUT (Mode 1)
		d.cmd(0x20)
		d.waitBusy(5000)
		// Spoof temperature register to 90°C
		d.cmd(0x1A)
		d.data(0x5A) // temp[11:4] = 0x5A
		d.data(0x00) // temp[3:0] = 0
		// Reload LUT from OTP using spoofed temp (selects WS7)
		d.cmd(0x22)
		d.data(0x91) // load LUT Mode 1 (no temp sensor read, uses register)
		d.cmd(0x20)
		d.waitBusy(5000)
	}
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

func (d *EPD) Display() error {
	d.setWindow(0, 0, epdW-1, epdH-1)
	d.setPointerNoWait(0, 0)

	// Let any in-flight USBD DMA complete before bulk SPI.
	time.Sleep(5 * time.Millisecond)

	d.cmd(0x24)
	d.dataBlock(d.buffer[:])

	d.setPointerNoWait(0, 0)
	d.cmd(0x26)
	d.dataBlock(d.redBuffer[:])

	if useFastRefresh {
		// Re-spoof temperature and reload fast LUT before each display update.
		// cmd 0x32 (custom LUT) does NOT affect the active display — only OTP
		// loading populates the active waveform register on the SSD1680.
		d.cmd(0x1A)
		d.data(0x5A) // 90°C
		d.data(0x00)
		d.cmd(0x22)
		d.data(0x91) // load LUT Mode 1 using spoofed temp → selects WS7
		d.cmd(0x20)
		d.waitBusy(5000)
		// Display using the loaded fast LUT (no reload)
		d.cmd(0x22)
		d.data(0xC7) // display Mode 1, no LUT/temp reload
	} else {
		d.cmd(0x22)
		d.data(0xF7) // load temp + LUT from OTP + display Mode 1
	}
	d.cmd(0x20) // master activation

	ms, timedOut := d.waitBusy(25000)
	d.LastRefreshMS = ms
	d.LastTimeout = timedOut
	return nil
}

func (d *EPD) ClearDisplay() {
	d.ClearBuffer()
	d.Display()
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
