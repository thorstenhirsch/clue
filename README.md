# tiny-claude-eink-display

A physical e-ink display that shows your Claude Pro rate-limit usage in real time — 5-hour and weekly token windows, with segmented progress bars and reset countdowns.

```
┌──────────────────────────────────────────┐
│ CLAUDE PRO                               │
│ ─────────────────────────────────────    │
│ 5-HOUR                   Resets 2h 14m   │
│ ▕█████████████         ▏ 48%     (RED)   │
│ 1.2M / 3.2M tokens                       │
│ - - - - - - - - - - - - - - - - - - - -  │
│ WEEKLY                   Resets 4d 11h   │
│ ▕██████████████████    ▏ 72%     (BLACK) │
│ 18.4M / 25.6M tokens                     │
└──────────────────────────────────────────┘
```

The 5-hour window renders in **red** and the weekly window in **black**, using the tri-color e-ink display's native red channel.

## Hardware

- [nice!nano](https://nicekeyboards.com/nice-nano/) (nRF52840 microcontroller)
- [WeAct Studio 2.9" tri-color e-ink display](https://github.com/WeActStudio/WeActStudio.EpaperModule) (Black/White/Red, SSD1680) — 296×128 pixels

### Wiring

| Display Pin | nice!nano Pin |
|-------------|---------------|
| DIN (MOSI)  | P0.24         |
| CLK (SCK)   | P0.22         |
| CS          | P0.06         |
| DC          | P0.08         |
| RST         | P0.17         |
| BUSY        | P0.20         |

  ┌─────────────┬───────────────────────┐
  │ E-Paper Pin │     nice!nano Pin     │
  ├─────────────┼───────────────────────┤
  │ SDA         │ P0.24 (D5) — SPI MOSI │
  ├─────────────┼───────────────────────┤
  │ SCL         │ P0.22 (D4) — SPI SCK  │
  ├─────────────┼───────────────────────┤
  │ CS          │ P0.06 (D1)            │
  ├─────────────┼───────────────────────┤
  │ D/C         │ P0.08 (D0)            │
  ├─────────────┼───────────────────────┤
  │ RES         │ P0.17 (D2)            │
  ├─────────────┼───────────────────────┤
  │ BUSY        │ P0.20 (D3)            │
  ├─────────────┼───────────────────────┤
  │ VCC         │ VCC (3.3V)            │
  ├─────────────┼───────────────────────┤
  │ GND         │ GND                   │
  └─────────────┴───────────────────────┘

## Prerequisites

- [Go](https://go.dev/) (1.26+)
- [TinyGo](https://tinygo.org/) (for firmware only)
- [Claude Code](https://claude.ai/code) — you need to be logged in so that `~/.claude/.credentials.json` exists

## Quick Start

### 1. Flash the firmware

Put your nice!nano into bootloader mode (double-tap reset), then:

```sh
make flash
```

Or build the UF2 and copy it manually:

```sh
make firmware
# copy clue.uf2 to the mounted drive
cp clue.uf2 /run/media/${USER}/NICENANO/ && sync
```

### 2. Build and run clue

```sh
make clue
./clue
```

That's it. `clue` reads your Claude credentials automatically and starts pushing usage data to the display.

## Using clue

`clue` (**cl**aude **u**sage **e**-ink) is the host-side daemon that polls the Claude API and sends rate-limit data to the device over USB serial.

### How authentication works

`clue` reads OAuth credentials from `~/.claude/.credentials.json` — the same file that [Claude Code](https://claude.ai/code) writes when you log in. No API keys, no manual token setup. If you're logged into Claude Code, `clue` just works.

If the token has expired, `clue` will tell you:

```
Access token expired at 2026-06-19T18:27:11+02:00.
Run 'claude' to refresh.
```

Just open Claude Code to refresh the token, then restart `clue`.

### Flags

```
--port string    Serial port (e.g. /dev/ttyACM0). Auto-detected if omitted.
--interval dur   Polling interval (default 30s).
```

### Examples

```sh
# Auto-detect port, poll every 30s (default)
./clue

# Specify port and poll every minute
./clue --port /dev/ttyACM0 --interval 1m

# Fast updates
./clue --interval 10s
```

### Running as a systemd service

To keep `clue` running in the background:

```ini
# ~/.config/systemd/user/clue.service
[Unit]
Description=Claude usage e-ink display
After=default.target

[Service]
ExecStart=%h/path/to/clue
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
```

```sh
systemctl --user daemon-reload
systemctl --user enable --now clue
```

## Build Targets

| Command        | Description                              |
|----------------|------------------------------------------|
| `make all`     | Build firmware and clue                  |
| `make clue`    | Build the host daemon                    |
| `make firmware`| Build firmware → `clue.uf2`              |
| `make flash`   | Flash firmware directly to nice!nano     |
| `make clean`   | Remove build artifacts                   |

## Project Structure

```
cmd/clue/        Host daemon — reads credentials, polls API, sends data over serial
claude/          Go package — API client and credential loader
firmware/        TinyGo firmware — display rendering and serial protocol
```
