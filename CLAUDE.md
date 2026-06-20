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
| Host→Device | `U:h5used:h5limit:w1used:w1limit:h5reset:w1reset` | Usage data (all int64) |
| Host→Device | `E` | Auth error — token expired |

Legacy firmware protocol messages (`G`, `T:`, `S:`, `K`, `F`) exist in the firmware for token storage but are unused by `clue`.

## Display Architecture

- **WeAct Studio 2.9" tri-color (Black/White/Red)**, SSD1680 controller — NOT a Waveshare B&W display
- Custom driver in `firmware/epd.go` (no TinyGo waveshare-epd dependency); derived from GxEPD2_290_C90c
- **Standard SSD1680 BUSY polarity**: BUSY HIGH = busy, LOW = ready (confirmed via GxEPD2_290_C90c reference)
- 296×128 pixels, landscape (270° rotation). Dual RAM: black/white (cmd 0x24) + red (cmd 0x26)
- Red polarity controlled by single `redPixelClearsBit` constant in `epd.go` — flip it if red renders inverted
- 5-hour usage section renders in **red**, weekly section in **black**; solid framed progress bars with big "used %" number
- Large percentage numbers use custom 5x7 bitmap glyphs scaled 2× (`bigGlyphs` in `display.go`)
- All rendering uses `tinyfont` (proggy font) and `tinydraw` primitives — no framebuffer image package
- Display only refreshes when usage data actually changes (battery saver); no animation or periodic refresh

## Flash Token Storage

Firmware stores tokens at flash offset 0 with a 4-byte header: magic bytes `TK` + big-endian uint16 length, followed by the raw token string. Max 2048 bytes. This is a legacy mechanism — `clue` reads credentials from the filesystem instead.
