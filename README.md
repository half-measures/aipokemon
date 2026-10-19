# AI game harness

An AI (Claude or a local model) plays Pokemon Red. Plan: https://claude.ai/code/artifact/c2f1c264-1876-4c69-87cd-290376e330fa

- `emu/` is a thin Python HTTP server around the PyBoy emulator. It knows buttons, frames, pixels and bytes, and nothing about Pokemon.
- `harness/` is the Go program: the observe, decide, act loop and the brains.

Status: milestones 1 to 5 (emulator, emulator server, Go loop with a random agent, RAM decoder, step log and dashboard).

## Setup (Windows 11, PowerShell)

1. Install [uv](https://docs.astral.sh/uv/) (`winget install astral-sh.uv`) and Go 1.24+ (`winget install GoLang.Go`).
2. Put your Pokemon Red ROM at `rom\pokemon_red.gb`. ROMs are gitignored and never committed.

## Milestone 1: play it yourself

```powershell
cd emu
uv run server.py --rom ..\rom\pokemon_red.gb --play
```

Arrows move, A key = A, S key = B, Enter = Start, Backspace = Select. Done when you can walk out of your house.

## Milestone 2: emulator server

```powershell
cd emu
uv run server.py --rom ..\rom\pokemon_red.gb
```

In a second terminal (`curl.exe`, not PowerShell's `curl` alias):

```powershell
curl.exe http://127.0.0.1:8765/health
curl.exe -X POST http://127.0.0.1:8765/press -d '{\"buttons\":[\"a\"],\"hold\":8,\"wait\":16}'
curl.exe -o screen.png http://127.0.0.1:8765/screen.png
curl.exe "http://127.0.0.1:8765/mem?addr=0xD35E&len=1" -o map.bin
```

The `\"` escaping is for Windows PowerShell 5.1. On PowerShell 7.3 or newer, use plain quotes: `-d '{"buttons":["a"]}'`.

| Endpoint | Does |
| --- | --- |
| `GET /health` | ROM title and frame count |
| `POST /press` `{"buttons":["a"],"hold":8,"wait":16}` | Hold buttons for `hold` frames, release, run `wait` more frames |
| `GET /screen.png` | Current 160x144 frame |
| `GET /mem?addr=0xD35E&len=1` | Raw bytes from memory |
| `POST /state/save` / `/state/load` `{"slot":"name"}` | Save states, stored in `emu/states/` |

Tip: play to an interesting spot with `--play`, but save states from the server (`/state/save`) so the harness can reload them.

## Milestone 3: Go harness with a random agent

With the server running:

```powershell
cd harness
go run ./cmd/harness --agent=random --steps=1000 --snap-every=100
```

Screenshots land in `harness\runs\<timestamp>\`. Useful flags: `--load-state <slot>`, `--steps 0` (run until Ctrl+C), `--seed N` (repeat a random run exactly).

## Milestone 4: RAM decoder

`internal/game/pokered` decodes map, position, facing, battle, party, badges, money and Pokedex count. Addresses come from pret/pokered's symbol file; map and species names are generated from its source (`names_gen.go`).

```powershell
cd harness
go test ./...          # decoder tests use hand-built RAM, no emulator needed
go run ./cmd/peek      # print what the agent sees right now (server must be running)
go run ./cmd/peek -json
```

Memory does not say whether a menu or dialog is open; the screenshot covers that.

## Milestone 5: step log and dashboard

Every step goes into `runs.db` (SQLite): the screenshot the agent saw, the decoded state, the action, its reasoning and timing.

```powershell
cd harness
go run ./cmd/harness --steps 2000 --delay 100ms --web :8080
```

Open the URL it prints. The run page follows the game live; untick "Follow live" or drag the slider to scrub back through every step. `--delay` slows fast agents down so you can watch. To browse old runs without the emulator: `go run ./cmd/dash`.

Because it is just SQLite, questions about runs are SQL:

```sql
-- steps spent on each map in run 1
SELECT map_name, COUNT(*) FROM steps WHERE run_id = 1 GROUP BY map_name ORDER BY 2 DESC;
```

## Layout

```text
emu/server.py                        PyBoy + HTTP endpoints
harness/cmd/harness/main.go          the loop: observe, decide, act
harness/internal/emulator/           Emulator interface + HTTP client
harness/internal/agent/agent.go      Agent interface, Observation, Action
harness/internal/agent/random/       random-button brain
harness/internal/game/pokered/       RAM decoder, map and species names
harness/internal/runlog/             SQLite step log
harness/internal/web/                dashboard: templates, htmx (vendored), SSE broker
harness/cmd/peek, harness/cmd/dash   small helper commands
```

Next: milestone 6, the Claude agent.
