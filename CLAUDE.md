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
| Host→Device | `M:0\|1\|2` | Set B/W partial mode: 0=OTP 0xFF, 1=diffLUT 0xCF, 2=full OTP fallback |
| Host→Device | `P` | Test partial refresh with current data |

Reset fields: `h5resetMin` = minute-of-day (0-1439, or -1), `w1resetDay` = weekday (0=Sun..6=Sat, or -1), `w1resetMin` = minute-of-day. The host computes these from the API's RFC3339 reset timestamps and sends absolute local clock times so the firmware doesn't need an RTC.

## Host Daemon (`./clue`)

- Waits for serial device to appear (polls every 2s) — can be started before plugging in the nice!nano
- Resilient to USB disconnect/reconnect: detects serial I/O errors, closes the port, and loops back to device detection. Designed to run as a long-lived systemd service
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
- **Threshold-driven red**: both sections default to **black**; bar+title turn **red** at **≥80%** usage. Big %, reset time, and token stats always stay black (they change frequently — keeping them B/W enables fast partial refresh). Red pixels only grow (title switches black→red once; bar fill adds red as it grows). When usage drops below 80% (reset), red must be cleared via a full OTP refresh — `RefreshSmart` detects this automatically via `anyRedCleared()`. Progress bars + big 2×-scaled digit glyphs
- **CRITICAL — Reversed voltage polarity on this panel**: on the GDEY029Z94, **VSH1 drives BLACK** and **VSL drives WHITE** — the reverse of the SSD1680 datasheet naming convention (which says VSH1 = "source high" and VSL = "source low"). The OTP waveform is calibrated for this panel and produces correct output. **All custom LUTs must use VSH1 (01) for black and VSL (10) for white.** VSH2 (11) drives red as expected. Getting this wrong causes B/W inversion — this was the root cause of every B/W inversion bug we encountered. Never assume VSH1=white, VSL=black from the datasheet — verify against the OTP's behaviour on the actual panel
- **Smart refresh engine** with pixel-level diffing (CLUE-FW-19):
  - `RefreshSmart()` compares working buffers against last-displayed snapshot, picks cheapest tier
  - Full-screen OTP (`0xF7`) for init / 4am / every 8 partials (anti-ghost) / red pixel removal
  - Tri-color custom LUT (`triLUT` + `0xC7`, Mode 1, 2-group, 3-pass) when red pixels are added — Group 0 clears BW residue with VSL (white), Group 1 drives VSH2 (red) for saturation
  - Fast B/W refresh (`diffLUT` + `0xC7`, Mode 1, 5-pass) for B/W-only changes — all pixels reinforced to prevent fading
  - Pixel diff skips refresh entirely when nothing changed
- **All custom refreshes use Mode 1 (`0xC7`)** — Mode 2 (`0xCF`, differential) was abandoned because its LUT index mapping depends on controller state that the OTP modifies unpredictably, causing B/W inversion on the first custom refresh after any 0xF7
- **Controller standby**: every refresh sequence (0xF7/0xC7) ends by disabling clock+analog. All refresh functions call `wake()` (0xC0 + master activation) before SPI writes. `DisplayFull` also calls `initRegisters()` after OTP to reset any registers the OTP modified
- **Critical voltage register insight**: the SSD1680 automatically loads VGH/VSH/VSL/VCOM from OTP during any 0xF7 refresh. These values persist in the registers. Custom LUTs (cmd 0x32 + 0xC7) reuse them — **never write 0x03/0x04/0x2C/0x3F manually**
- **Error display**: auth errors (`E` command) render in black-only text ("Token Expired or Revoked" / "Run 'claude' to re-authenticate") via `RefreshSmart()` — fast B/W partial, no red ink, no full OTP needed

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

On this panel, the LUT index is `(0x24 << 1) | 0x26` — **0x24 is the "old" frame, 0x26 is the "new" frame**. (Many SSD1680 documents list the reverse assignment; the correct one was derived from observed panel behaviour.)

| 0x24 (old) | 0x26 (new) | Transition | LUT |
|------------|------------|------------|-----|
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

**Root cause**: No prior 0xF7 refresh had run to populate the voltage registers from OTP. The LUT's VS values (01=VSH1, 10=VSL, 11=VSH2) are REFERENCES to voltages generated by the analog block. Without those registers populated, the source driver has no voltages to apply.

**Critical lesson**: **cmd 0x32 DOES write the live LUT register** (confirmed by Adafruit, Zephyr, NuttX). The actual fix is simpler than we thought: run one 0xF7 refresh first (which auto-loads OTP voltages), then use custom LUTs freely — the voltage registers persist. **Never write 0x03/0x04/0x2C/0x3F manually** — the Adafruit values (0x41/0xAE/0x32) are for a different panel and caused the inversions in Attempts 3-4.

### Attempt 3: Custom LUT WITH Adafruit Voltage Registers (FAILED — ROOT CAUSE FOUND)

**Approach**: Write Adafruit voltage registers (`0x03→0x17`, `0x04→0x41,0xAE,0x32`, `0x2C→0x36`, `0x3F→0x22`) + custom LUT + 0xC7.

**Result**: Inverted B/W, no proper clearing. Same symptoms as Attempt 2.

**Root cause (resolved June 2026)**: The Adafruit voltage values are for a DIFFERENT panel (Adafruit 2.9" SSD1680). Our WeAct/GDEY029Z94 panel needs its own OTP-calibrated voltages. **Writing any voltage registers at all was the mistake** — it overwrote the correct OTP values that 0xF7 had loaded. GxEPD2's SSD1680 drivers **never** write 0x03/0x04/0x2C, confirming this is the correct approach.

### Attempt 5: Smart Refresh Engine (CLUE-FW-19 — CURRENT)

**Approach**: Run one 0xF7 at init (loads correct OTP voltages into registers), then never write voltage registers again. Custom LUTs via cmd 0x32 reuse the persisted OTP voltages. Pixel-level diffing selects the cheapest refresh tier. `DisplayFull` re-sends cmd 0x21 `{0x00, 0x80}` after OTP completes to restore B/W polarity; `refreshTriColor` re-sends it before each pass.

**Key insight**: The SSD1680 voltage registers persist between refreshes. A 0xF7 refresh auto-loads them from OTP. Subsequent custom LUT operations reference the same correct voltages — no manual writes needed. This is how GxEPD2 (mono partial) works: OTP loads voltages, then `0xFC` (OTP Mode 2) reuses them.

**B/W partial flow** (Mode 2, full-screen): write OLD B/W (`dispBuffer`) to **0x24** and NEW B/W (`buffer`) to **0x26**, trigger with 0xCF. On this panel `0x24` is the "old"/high bit and `0x26` the "new"/low bit for Mode 2 LUT selection (the reverse of what many SSD1680 docs state — confirmed by panel behaviour). Unchanged pixels map to LUT0/LUT3 = VSS (no drive, no flicker). Both full buffers are written each time (~19ms SPI overhead at 4MHz) to avoid stale RAM outside a dirty window. Sub-second total.

**Red/tri-color flow** (Mode 1, 3-pass): write full BW+red to 0x24/0x26, then trigger `triLUT` + 0xC7 three times to build up red pigment saturation (~9s total). Each pass reinforces existing red pixels and establishes new ones. Red is additive-only between resets — the no-clear LUT never erases red.

**Red removal**: When usage drops below 80% at reset, red pixels need clearing. `anyRedCleared()` detects bits that were set in the displayed red buffer but cleared in the new one. `RefreshSmart` forces a full OTP refresh in this case — only a 0xF7 can reliably erase red.

**Debug harness**: `M:0` (OTP 0xFF), `M:1` (diffLUT 0xCF, default), `M:2` (full OTP) — switch via serial without reflashing.

### Current State (CLUE-FW-19)

- `DisplayFull()` uses **0xF7 (OTP)** — reliable, correct colors, auto-loads voltage registers. Re-sends cmd 0x21 `{0x00, 0x80}` after OTP completes to restore B/W polarity
- `RefreshSmart()` picks the cheapest tier via pixel-level diffing:
  - Full-screen OTP for init / 4am / every 8 partials / red pixel removal (`anyRedCleared()`)
  - Tri-color custom LUT (`triLUT` + `0xC7`, 3-pass) for additive red changes (~9s)
  - Full-screen differential (`diffLUT` + `0xCF`, OLD→0x24, NEW→0x26) for B/W-only changes — sub-second, no flicker, **no voltage register writes**
  - Skip when nothing changed
- `M:0|1|2` serial command switches B/W partial mode without reflashing
- Nightly 4am forced full refresh via `F` serial command
- Error screen (`E` command) renders in black-only via `RefreshSmart()` — fast B/W partial

### What to Try Next

1. **Read OTP voltage values**: Use cmd 0x2D after an OTP load to read back the panel's actual VGH/VSH1/VSH2/VSL/VCOM. Could enable a custom `fullLUT` + `0xC7` for faster full refreshes (cutting ~15s to ~2-4s).

2. **Optimize the 4am/init full refresh**: Once the correct OTP voltages are known, write a custom `fullLUT` with fewer groups and use `0xC7` (no OTP reload). Gate behind `M:` harness.

3. **Tune tri-color pass count**: The 3-pass `refreshTriColor` builds adequate red saturation in ~9s. With known OTP voltages, a stronger single-pass LUT might achieve the same result faster.

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
