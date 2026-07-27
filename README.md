# CLUE — Coding-agent usage on e-ink

A physical e-ink display connected to a nice!nano (nRF52840) that shows either Claude Code or OpenAI Codex rate-limit usage. Claude Code is the default build target; Codex is selected at build time.

```
┌──────────────────────────────────────────┐
│ CLAUDE PRO / OPENAI CODEX                │
│ ─────────────────────────────────────    │
│ 5-HOUR                         14:30     │
│ ▕█████████████         ▏ 48%             │
│ 48% RATE-LIMIT UTILIZATION               │
│ - - - - - - - - - - - - - - - - - - - -  │
│ WEEKLY                   Wed 14:30       │
│ ▕██████████████████████▏ 85%             │
│ 85% RATE-LIMIT UTILIZATION               │
└──────────────────────────────────────────┘
```

Both sections render in **black** by default. When a section's usage reaches **≥80%**, its progress bar and title turn **red** using the tri-color e-ink display's native red channel. Percentages, reset times, and utilization details stay black for fast partial refresh. Window labels come from the provider's reported duration (`5-HOUR`, `WEEKLY`, and so on).

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
- For the default Claude build: [Claude Code](https://claude.ai/code), logged in so `~/.claude/.credentials.json` exists
- For the Codex build: [Codex CLI](https://developers.openai.com/codex/cli), signed in with ChatGPT and configured for file credential storage

## Quick Start

### Claude Code (default)

Put your nice!nano into bootloader mode (double-tap reset), then build and flash:

```sh
make flash
make clue
./clue
```

Or build the UF2 and copy it manually:

```sh
make firmware
# copy clue.uf2 to the mounted drive
cp clue.uf2 /run/media/${USER}/NICENANO/ && sync
```

### OpenAI Codex

Configure Codex to keep its cached login in a file:

```toml
# ~/.codex/config.toml
cli_auth_credentials_store = "file"
```

Then authenticate and build both components with the same provider:

```sh
codex login
make PROVIDER=codex flash
make PROVIDER=codex clue
./clue
```

`make codex` is a shortcut for building both Codex artifacts. `PROVIDER=codex make all` is equivalent to `make PROVIDER=codex all`.

## Using clue

`clue` is the host-side daemon that polls the selected provider and sends rate-limit data to the device over USB serial. It is resilient to USB disconnect/reconnect: if the nice!nano is unplugged, `clue` closes the port and waits for the device to reappear.

The reading-light LED is off at boot. It turns on when a host daemon connects and turns off after 60 seconds without host traffic, including when `clue` is killed without a graceful shutdown.

### How authentication works

The Claude build reads OAuth credentials from `~/.claude/.credentials.json`, the same file Claude Code writes.

The Codex build reads `$CODEX_HOME/auth.json` (or `~/.codex/auth.json`) and requires ChatGPT subscription login. It intentionally does not support API-key login, because API usage has different rate limits, or keyring-only storage, because `clue` never asks Codex to export secrets. It only reads credentials and never refreshes or rewrites them.

Codex usage is read from the same `GET /backend-api/wham/usage` route used by the current open-source Codex client. This direct route is not a public API and may change in a future Codex release; all related code is isolated in the `codex` package.

If the token has expired, `clue` will tell you — both in the terminal and on the e-ink display:

```
Access token expired at 2026-06-19T18:27:11+02:00.
Run 'claude' to refresh.
```

For Codex, run `codex login`. The display uses provider-specific recovery text in black, preserving the fast error refresh.

### Flags

```
--port string    Serial port (e.g. /dev/ttyACM0). Auto-detected if omitted.
```

### Examples

```sh
# Auto-detect port
./clue

# Specify port
./clue --port /dev/ttyACM0
```

### Running as a systemd service

`clue` is designed to run as a long-lived daemon — it handles USB disconnect/reconnect gracefully and shows errors on the display (so you don't need to watch the terminal). To run it in the background:

```ini
# ~/.config/systemd/user/clue.service
[Unit]
Description=Coding-agent usage e-ink display
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

| Command | Description |
|---------|-------------|
| `make all` | Build Claude firmware and host daemon |
| `make PROVIDER=codex all` | Build Codex firmware and host daemon |
| `make codex` | Shortcut for the Codex `all` build |
| `make clue` | Build the selected host daemon |
| `make firmware` | Build selected firmware → `clue.uf2` |
| `make flash` | Flash selected firmware to nice!nano |
| `make test` | Test both host provider variants |
| `make clean` | Remove build artifacts |

## Project Structure

```
cmd/clue/        Shared host daemon plus build-tagged provider selection
provider/        Provider-neutral usage types and interface
claude/          Claude credential loader and API client
codex/           Codex credential loader and usage client
firmware/        Shared TinyGo display/serial code plus build-tagged branding
```
