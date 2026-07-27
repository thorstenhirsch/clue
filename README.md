# CLUE — Coding-agent usage on e-ink

A physical e-ink display connected to a nice!nano (nRF52840) that shows either Claude Code or OpenAI Codex rate-limit usage. The firmware is provider-independent, and `make all` builds one explicitly named host binary for each provider.

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

With no red displayed, B/W changes use a transition-selective ~670ms refresh
verified through 32 full-screen alternations. First red appearance uses the
~5.46s custom tri-color waveform. Removing red still uses a full refresh.

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
make clue-claude
./clue-claude
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

Then authenticate and build the Codex host binary. The same firmware works for both providers:

```sh
codex login
make codex
./clue-codex
```

Flash the firmware once with `make flash`. Switching providers later only requires running `./clue-claude` or `./clue-codex`; the selected host announces its provider over serial and the display updates its branding automatically. `make all` builds the universal firmware and both host binaries, with no provider argument required.

## Using clue

`clue-claude` and `clue-codex` are host-side daemons that poll their respective providers and send rate-limit data to the device over USB serial. They are resilient to USB disconnect/reconnect: if the nice!nano is unplugged, the daemon closes the port and waits for the device to reappear.

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
./clue-claude

# Specify port
./clue-codex --port /dev/ttyACM0
```

### Running as a systemd service

`clue` is designed to run as a long-lived daemon — it handles USB disconnect/reconnect gracefully and shows errors on the display (so you don't need to watch the terminal). To run it in the background:

```ini
# ~/.config/systemd/user/clue.service
[Unit]
Description=Coding-agent usage e-ink display
After=default.target

[Service]
ExecStart=%h/path/to/clue-claude
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
| `make all` | Build universal firmware, `clue-claude`, and `clue-codex` |
| `make clue` | Build both provider-specific host binaries |
| `make clue-claude` / `make claude` | Build only the Claude host binary |
| `make clue-codex` / `make codex` | Build only the Codex host binary |
| `make firmware` | Build universal firmware → `clue.uf2` |
| `make flash` | Flash universal firmware to nice!nano |
| `make test` | Test both host provider variants |
| `make clean` | Remove build artifacts |

## Experimental refresh lab

The firmware contains isolated, RAM-only waveform experiments for the exact
SSD1680 tri-color panel. They are never selected by the normal daemon. Stop the
daemon, build `clue-test`, and run the guided visual suite:

```sh
make clue-test
./clue-test --refresh-test all
```

The runner pauses after every reference and experimental image and prints what
to inspect: black density, white cleanliness, red saturation, old-image
shadows, and timing. It always finishes with a true OTP recovery refresh.
Individual experiments and tunables are available through `--refresh-test bw`,
`red-add`, `red-clear`, or `cadence`, plus `--bw-reps`, `--bw-frames`,
`--red-passes`, and `--red-rp`. The cadence test pauses after 8, 16, and 32
alternating updates. Red removal is intentionally experimental and must not be
promoted without repeated visual validation.

## Project Structure

```
cmd/clue/        Shared host daemon plus build-tagged provider selection
provider/        Provider-neutral usage types and interface
claude/          Claude credential loader and API client
codex/           Codex credential loader and usage client
firmware/        Universal TinyGo display and serial firmware
```
