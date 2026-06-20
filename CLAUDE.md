# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

E-ink display that shows Claude Pro rate-limit usage (5-hour and weekly token windows). Two components communicate over USB serial:

- **firmware/** — TinyGo firmware for a nice!nano driving a WeAct Studio 2.9" tri-color (BWR) e-ink display (SSD1680)
- **cmd/clue/** — host daemon ("**cl**aude **u**sage **e**-ink") that reads OAuth credentials from `~/.claude/.credentials.json`, polls the Anthropic API for rate-limit utilization, and pushes usage data to the device over serial
- **claude/** — Go package: API client (`client.go`) and credential loader (`credentials.go`)

## Build Commands

```
make clue          # go build → clue binary
make firmware      # tinygo build → clue.uf2
make flash         # tinygo flash directly to connected nice!nano
make all           # firmware + clue
make clean         # remove artifacts
```

## Two Separate Go Modules

The firmware uses a **separate** `go.mod` under `firmware/` (TinyGo-specific dependencies: `tinygo.org/x/drivers`, `tinyfont`, `tinydraw`). The root `go.mod` covers the host-side `clue` tool only (`go.bug.st/serial`). Do not merge them — TinyGo and standard Go have different build constraints.

## Authentication & Usage Data

`clue` reads OAuth credentials from `~/.claude/.credentials.json` (the same file Claude Code writes). To get rate-limit utilization, it makes a minimal `POST /v1/messages` call to `api.anthropic.com` (1 token of Haiku, essentially free) and reads the undocumented `anthropic-ratelimit-unified-*` response headers for 5-hour and 7-day utilization percentages. No separate usage API endpoint is needed. If the token is expired, the user just runs `claude` to refresh it.

## Serial Protocol

Newline-delimited ASCII messages between device and host:

| Direction | Message | Meaning |
|-----------|---------|---------|
| Device→Host | `R` | Device ready (has stored token) |
| Device→Host | `N` | No token stored |
| Host→Device | `U:h5used:h5limit:w1used:w1limit:h5resetMin:w1resetDay:w1resetMin` | Usage data (7 int64 fields) |
| Host→Device | `E` | Auth error — token expired |
| Host→Device | `F` | Force full OTP refresh with current data |
| Host→Device | `G` | Request token/status |

Reset fields: `h5resetMin` = minute-of-day (0-1439, or -1), `w1resetDay` = weekday (0=Sun..6=Sat, or -1), `w1resetMin` = minute-of-day. The host computes these from the API's RFC3339 reset timestamps and sends absolute local clock times so the firmware doesn't need an RTC.

## Host Daemon (`./clue`)

- Waits for serial device to appear (polls every 2s) — can be started before plugging in the nice!nano
- Polls API every 30s, only sends data when usage actually changes
- Nightly 4am full OTP refresh (`F` command) to clear e-ink ghosting
- Reset time log lines only printed when the 5h reset time changes

## Display Architecture

- **WeAct Studio 2.9" tri-color (Black/White/Red)**, SSD1680 controller
- **Panel**: GDEY029Z94 (Good Display), 296×128 pixels
- Custom driver in `firmware/epd.go` (no TinyGo waveshare-epd dependency); derived from GxEPD2_290_C90c
- **SSD1680 has 176 source outputs** but our panel uses only 128. The active area maps to sources S8-S167, configured via cmd 0x21 byte 2 = 0x80 (B[7]=1)
- **BUSY polarity**: HIGH = busy, LOW = ready
- Landscape via 270° rotation. Dual RAM: black/white (cmd 0x24) + red (cmd 0x26)
- Red polarity: `redPixelClearsBit = false` (set bit = red pixel). Red buffer inits to 0x00
- 5-hour section in **red**, weekly in **black**; progress bars + big 2×-scaled digit glyphs
- Two refresh modes: `DisplayFull()` (OTP, 0xF7) and `DisplayDiff()` (custom LUT, 0xCF)
- Display only refreshes when usage data actually changes; differential mode when only B/W content changed

## Flash Token Storage

Firmware stores tokens at flash offset 0 with a 4-byte header: magic bytes `TK` + big-endian uint16 length, followed by the raw token string. Max 2048 bytes. This is a legacy mechanism — `clue` reads credentials from the filesystem instead.

---

## SSD1680 E-Ink Refresh Optimization — Technical Reference

This section documents all approaches attempted to reduce the e-ink display's refresh flickering, what worked, what failed, and why. **Read this before attempting further LUT/refresh optimization.**

### Hardware Specs

- **Controller**: Solomon Systech SSD1680 (Rev 0.14, Jun 2019)
- **Panel**: GDEY029Z94 / WeAct Studio 2.9" tri-color (Black/White/Red)
- **Resolution**: 128×296 (physical portrait), displayed as 296×128 (landscape, 270° rotation)
- **RAM**: 176×296 bits for B/W + 176×296 bits for Red (controller supports 176 sources)
- **SPI**: 4MHz, Mode 0, MSB first. nRF52840 SPIM with bulk `Tx()` (not per-byte `Transfer()`)
- **OTP**: 36 waveform settings (WS0-WS35) × temperature ranges (TR0-TR35)
- **OTP refresh time**: ~15 seconds at room temperature, ~15 visible clearing/flashing phases

### SSD1680 Command Reference (key commands)

| Cmd | Name | Notes |
|-----|------|-------|
| 0x01 | Driver Output Control | MUX=0x0127 (296 gates) |
| 0x03 | Gate Driving Voltage | VGH, range 10V-20V |
| 0x04 | Source Driving Voltage | 3 bytes: VSH1, VSH2, VSL |
| 0x11 | Data Entry Mode | 0x03 = X inc, Y inc |
| 0x12 | SW Reset | BUSY goes high during reset |
| 0x18 | Temperature Sensor | 0x80 = internal sensor |
| 0x1A | Write Temperature Register | 12-bit, DegC = value/16 |
| 0x21 | Display Update Control 1 | Byte A: BW/Red RAM options. **Byte B[7]**: source output mode (0=S0-S175, 1=S8-S167) |
| 0x22 | Display Update Control 2 | Sequence control — see bit table below |
| 0x24 | Write B/W RAM | Sequential pixel data |
| 0x26 | Write Red RAM | Sequential pixel data |
| 0x2C | Write VCOM Register | DCVCOM voltage |
| 0x32 | Write LUT Register | 153 bytes: VS + TP/SR/RP + FR + XON |
| 0x37 | Display Option | 10 bytes; byte 5 bit 6 = ping-pong RAM enable |
| 0x3C | Border Waveform | 0x05 for this panel |
| 0x3F | End Option (EOPT) | 0x22 = normal end |
| 0x44/0x45 | RAM X/Y Window | Address range for partial writes |
| 0x4E/0x4F | RAM X/Y Counter | Starting address |

### cmd 0x22 — Display Update Control 2 — Bit Breakdown

This is the most critical register. Community-verified bit meanings (confirmed by Adafruit, Zephyr, GxEPD2, Arduino forum analysis):

| Bit | Hex | Meaning |
|-----|-----|---------|
| A7 | 0x80 | Enable clock signal |
| A6 | 0x40 | Enable analog (charge pump) |
| A5 | 0x20 | Load temperature value from sensor |
| A4 | 0x10 | **Load LUT from OTP** — MUST be 0 when using custom cmd 0x32 LUT |
| A3 | 0x08 | **Mode 2** (differential/partial — compares old vs new RAM) |
| A2 | 0x04 | Run display refresh |
| A1 | 0x02 | Disable analog |
| A0 | 0x01 | Disable clock |

Key values:

| Value | Decoded | Use |
|-------|---------|-----|
| 0xF7 | clk+analog+temp+OTP LUT+display+disable | **Full OTP refresh** (GxEPD2 `_Update_Full`) — reliable, ~15s |
| 0xC7 | clk+analog+display+disable (A4=0, A5=0) | **Custom LUT full refresh** — uses cmd 0x32 LUT, no OTP reload |
| 0xCF | clk+analog+Mode2+display+disable (A4=0) | **Custom LUT differential** — only drives changed pixels |
| 0xF4 | clk+analog+temp+OTP LUT+display (no disable) | **Display Base** — establishes reference for differential |
| 0x1C | OTP LUT+Mode2+display (no clk/analog enable) | **Waveshare partial** — requires prior power-on |
| 0xB1 | clk+temp+OTP LUT+disable clk | **Load OTP LUT** (Mode 1, no display, no analog) |
| 0x91 | clk+OTP LUT+disable clk | **Load OTP LUT** using register temp (no sensor read) |

### LUT Register Format (cmd 0x32, 153 bytes)

```
Bytes 0-59:   VS section — 5 LUTs × 12 groups (1 byte each)
              Layout: LUT-first. Bytes 0-11 = LUT0 groups 0-11,
              bytes 12-23 = LUT1 groups 0-11, ..., bytes 48-59 = LUT4 groups 0-11.
              Each byte: D7-D6=PhaseA, D5-D4=PhaseB, D3-D2=PhaseC, D1-D0=PhaseD
              VS values: 00=VSS(ground), 01=VSH1(+), 10=VSL(-), 11=VSH2(red)

Bytes 60-143: TP/SR/RP section — 12 groups × 7 bytes
              Per group: TP[A], TP[B], SR[AB], TP[C], TP[D], SR[CD], RP
              TP = phase timing (0-255 frames, 0=skip)
              RP = repeat count (0=1 rep, 1=2 reps, ..., 255=256 reps)
              SR = state repeat for sub-phases

Bytes 144-149: FR section — frame rate, nibble-packed (FR[0]<<4|FR[1], ...)
              FR range 0-7 (0=slowest/25Hz, 7=fastest/200Hz)

Bytes 150-152: XON section — gate scan selection (packed bits, usually all 0)
```

Separate voltage registers (NOT part of cmd 0x32, must be set independently):
- 0x03: VGH (1 byte) — gate driving voltage
- 0x04: VSH1, VSH2, VSL (3 bytes) — source driving voltages
- 0x2C: VCOM (1 byte) — common electrode voltage
- 0x3F: EOPT (1 byte) — LUT end option

### LUT mapping — tri-color non-differential (Mode 1)

| Red RAM | BW RAM | Color | LUT |
|---------|--------|-------|-----|
| 0 | 0 | Black | LUT0 |
| 0 | 1 | White | LUT1 |
| 1 | 0 | Red | LUT2 |
| 1 | 1 | Red | LUT3 (=LUT2) |

### LUT mapping — differential (Mode 2)

| Old BW | New BW | Transition | LUT |
|--------|--------|------------|-----|
| 0 | 0 | Same (B→B) | LUT0 |
| 0 | 1 | Black→White | LUT1 |
| 1 | 0 | White→Black | LUT2 |
| 1 | 1 | Same (W→W) | LUT3 |

In differential mode, unchanged pixels (LUT0/LUT3) should use VS=00 (VSS, no drive) so they don't flash.

### OTP Temperature-Based Waveform Selection

The SSD1680 OTP stores waveforms for different temperature bands:
- WS4: 20-25°C (room temp, standard waveform)
- WS7: 33-127.9°C (high temp, faster phases)

Spoofing the temperature register (cmd 0x1A) selects a different band.

---

## Optimization Attempts — What Worked and What Failed

### Attempt 1: Temperature Trick (PARTIALLY WORKED)

**Approach**: Spoof temperature to 90°C via cmd 0x1A, reload OTP via 0x91 (uses register temp instead of sensor), display with 0xC7 (no OTP reload).

**Result**: The display refreshed noticeably faster, but still had the SAME number of clearing phases (~15). The high-temp waveform WS7 makes each phase shorter, not fewer. The user described it as "initially works quickly, but continues to flicker, then starts from full black."

**Why**: The OTP waveform has a fixed structure regardless of temperature. Temperature only affects phase timing, not phase count. WS7 has the same number of groups/repeats as WS4 — just faster per-frame.

**Code reference**: Waveshare `epd2in9b_V4` `Init_Fast()` does `0x1A→0x5A,0x00` + `0x22→0x91` + `0x20`.

### Attempt 2: Custom LUT via cmd 0x32 WITHOUT Voltage Registers (FAILED)

**Approach**: Write a custom 153-byte LUT with minimal clearing phases via cmd 0x32. Use 0xC7 (A4=0, don't reload OTP) for display. Pre-load OTP via 0xB1 to "get the voltage registers from OTP."

**Result**: Display showed **inverted B/W** (background black, text white) and **no clearing** (old content not erased). Adding clearing phases to the LUT made NO difference — the display behaved identically regardless of LUT content.

**Root cause**: The voltage registers (0x03 VGH, 0x04 VSH1/VSH2/VSL, 0x2C VCOM) were NOT populated. The LUT's VS values (01=VSH1, 10=VSL, 11=VSH2) are REFERENCES to voltages generated by the analog block. Without setting these registers, the source driver has no voltages to apply. The 0xB1 OTP preload likely didn't populate them because it doesn't enable the analog block (B6=0 in 0xB1).

**Critical lesson**: **cmd 0x32 DOES write the live LUT register** (confirmed by Adafruit CircuitPython, Zephyr ssd16xx, NuttX). But the LUT alone is useless — you MUST also set 0x03, 0x04, 0x2C, and 0x3F alongside it. The Adafruit driver always writes these explicitly when using custom LUTs.

**Adafruit-confirmed voltage values**: `0x03→0x17`, `0x04→0x41,0xAE,0x32`, `0x2C→0x36`, `0x3F→0x22`.

### Attempt 3: Custom LUT WITH Voltage Registers (NOT YET VERIFIED)

**Approach**: Write voltage registers explicitly + custom LUT + 0xC7. This is the `fullLUT` currently in `epd.go`.

**Status**: Code is in the firmware but DisplayFull() currently uses 0xF7 (OTP) as a reliability fallback. The custom fullLUT + voltage registers + 0xC7 path has NOT been tested on hardware yet. If someone wants to try it, change `DisplayFull()` to use 0xC7 instead of 0xF7 and ensure the voltage registers + fullLUT are written before the display command.

**Risk**: The voltage values (0x41/0xAE/0x32) are from Adafruit and may not be correct for the WeAct/GDEY029Z94 panel. Wrong voltages could produce weak colors or no display output.

### Attempt 4: Differential Mode 2 with diffLUT (NOT YET VERIFIED)

**Approach**: After a full OTP refresh (0xF7) establishes the displayed image, subsequent updates write only the B/W buffer (skip red RAM) + diffLUT + 0xCF (Mode 2 differential, custom LUT). Only changed pixels get driven — unchanged pixels don't flash.

**Status**: Code is in firmware (`DisplayDiff()`) but the differential path depends on the custom LUT working (Attempt 3). If the custom LUT + voltage registers work, this should give near-instant B/W-only updates with zero flashing on unchanged pixels.

**Limitation**: Red content can NOT be updated via differential mode. Red particles require full clearing cycles. When the 5-hour section changes, `DisplayFull()` is used instead.

### Current State

- `DisplayFull()` uses **0xF7 (OTP)** — reliable, correct colors, proper clearing, ~15s with ~15 flashing cycles
- `DisplayDiff()` uses **0xCF + diffLUT + voltage registers** — untested, should give fast B/W-only updates
- Smart selection in `handleMessage`: full refresh for first display / red changes / every 10 diffs; differential for B/W-only changes
- Nightly 4am forced full refresh via `F` serial command

### What to Try Next

1. **Verify Attempt 3**: Change `DisplayFull()` to write fullLUT + voltage registers + 0xC7 instead of 0xF7. If colors are correct, this cuts full refresh from ~15 phases to 4. If inverted/blank, the voltage values need calibration for this specific panel.

2. **Read OTP voltage values**: Use cmd 0x2D (OTP Register Read for Display Option) after an OTP load (0xF7) to read back the panel's actual VGH/VSH1/VSH2/VSL/VCOM values. Use these instead of Adafruit's values.

3. **Verify Attempt 4**: Once Attempt 3 works, test `DisplayDiff()` — should only flash changed B/W pixels.

4. **Waveshare Display_Base + Display_Partial**: Instead of custom LUT, try Waveshare's proven approach: `Display_Base` (0xF4) to establish reference, then `Display_Partial` (0x1C, writes only 0x24, OTP partial LUT). This uses OTP waveforms for both full and partial, avoiding custom voltage calibration.

5. **RAM Ping-Pong**: Enable via cmd 0x37 byte 5 = 0x40. May be needed for differential Mode 2 to properly compare old and new RAM banks.

### Sources

- [SSD1680 Datasheet (Crystalfontz)](https://www.crystalfontz.com/controllers/uploaded/SSD1680.pdf) — Rev 0.14, Jun 2019
- [SSD1680 Datasheet (Orient Display)](https://www.orientdisplay.com/wp-content/uploads/2022/08/SSD1680_v0.14.pdf)
- [Adafruit CircuitPython SSD1680 driver](https://github.com/adafruit/Adafruit_CircuitPython_SSD1680/blob/main/adafruit_ssd1680.py) — custom_lut parameter, 0xC7 usage, voltage register values
- [Adafruit SSD1680 partial update issue #28](https://github.com/adafruit/Adafruit_CircuitPython_SSD1680/issues/28)
- [GxEPD2 library](https://github.com/ZinggJM/GxEPD2) — GxEPD2_290_C90c reference driver
- [GxEPD2 partial update bug thread](https://forum.arduino.cc/t/gxepd2-gdey029t94-ssd1680-weird-partial-update-behaviour/1223604/9)
- [GxEPD2 cmd 0x22 bit analysis](https://forum.arduino.cc/t/gxepd2-lib-gdey029t94-undefined-update-sequence-options/1327151)
- [Waveshare 2.9" B Manual — V4 fast/partial refresh](https://www.waveshare.com/wiki/2.9inch_e-Paper_Module_(B)_Manual)
- [Waveshare e-Paper driver repo](https://github.com/waveshareteam/e-Paper) — epd2in9b_V4 source
- [Zephyr ssd16xx driver](https://github.com/zephyrproject-rtos/zephyr/blob/main/drivers/display/ssd16xx.c)
- [Zephyr ssd16xx regs](https://github.com/zephyrproject-rtos/zephyr/blob/main/drivers/display/ssd16xx_regs.h)
- [Zephyr partial refresh PR #48163](https://github.com/zephyrproject-rtos/zephyr/pull/48163)
- [Zephyr SSD1680 DT binding](https://docs.zephyrproject.org/latest/build/dts/api/bindings/display/solomon,ssd1680.html)
- [NuttX ssd1680.h — 0x22 bit definitions](https://github.com/apache/incubator-nuttx/blob/master/drivers/lcd/ssd1680.h)
- [u8g2 issue #1393 — Fast BWR LUTs for UC8151D (<4s tri-color)](https://github.com/olikraus/u8g2/issues/1393)
- [WeAct Studio Epaper Module repo](https://github.com/WeActStudio/WeActStudio.EpaperModule)
