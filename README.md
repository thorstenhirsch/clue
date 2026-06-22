# CLUE - Claude Usage (on) E-Ink (display)

A physical e-ink display connected to a nice!nano (NRF52840) that shows your Claude Pro rate-limit usage in real time — 5-hour and weekly token windows, with segmented progress bars and reset countdowns.

```
┌──────────────────────────────────────────┐
│ CLAUDE PRO                               │
│ ─────────────────────────────────────    │
│ 5-HOUR                         14:30     │
│ ▕█████████████         ▏ 48%             │
│ 1.2M / 3.2M tokens                       │
│ - - - - - - - - - - - - - - - - - - - -  │
│ WEEKLY                   Wed 14:30       │
│ ▕██████████████████████▏ 85%             │
│ 18.4M / 25.6M tokens                     │
└──────────────────────────────────────────┘
```

Both sections render in **black** by default. When a section's usage reaches **≥80%**, its progress bar and title turn **red** using the tri-color e-ink display's native red channel. The percentage, reset time, and token stats always stay black for fast partial refresh.

## Hardware

- [nice!nano](https://nicekeyboards.com/nice-nano/) (nRF52840 microcontroller)
- [WeAct Studio 2.9" tri-color e-ink display](https://github.com/WeActStudio/WeActStudio.EpaperModule) (Black/White/Red, SSD1680) — 296×128 pixels

### Wiring

| E-Paper Pin | nice!nano Pin |
|-------------|---------------|
| SDA / DIN   | P0.24 (D5) - SPI MOSI |
| SCL / CLK   | P0.22 (D4) - SPI SCK |
| CS          | P0.06 (D1) |
| D/C         | P0.08 (D0) |
| RES         | P0.17 (D2) |
| BUSY        | P0.20 (D3) |
| VCC         | VCC (3.3V) |
| GND         | GND |

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

`clue` (**cl**aude **u**sage **e**-ink) is the host-side daemon that polls the Claude API and sends rate-limit data to the device over USB serial. It is resilient to USB disconnect/reconnect — if the nice!nano is unplugged, `clue` detects the serial I/O error, closes the port, and waits for the device to reappear. No restart needed.

### How authentication works

`clue` reads OAuth credentials from `~/.claude/.credentials.json` — the same file that [Claude Code](https://claude.ai/code) writes when you log in. No API keys, no manual token setup. If you're logged into Claude Code, `clue` just works.

If the token has expired, `clue` will tell you — both in the terminal and on the e-ink display:

```
Access token expired at 2026-06-19T18:27:11+02:00.
Run 'claude' to refresh.
```

The display shows "Token Expired or Revoked" / "Run 'claude' to re-authenticate" in black text (fast refresh, no red ink). Just open Claude Code to refresh the token, then restart `clue`.

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

`clue` is designed to run as a long-lived daemon — it handles USB disconnect/reconnect gracefully and shows errors on the display (so you don't need to watch the terminal). To run it in the background:

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
