"""Thin HTTP wrapper around PyBoy.

Knows buttons, frames, pixels and bytes. Knows nothing about Pokemon;
all game logic lives in the Go harness.

    uv run server.py --rom ../roms/pokemon_red.gb          # headless API on :8765
    uv run server.py --rom ../roms/pokemon_red.gb --play   # play with the keyboard
"""

import argparse
import io
import json
import re
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse

from pyboy import PyBoy

BUTTONS = {"a", "b", "start", "select", "up", "down", "left", "right"}
SLOT_RE = re.compile(r"^[A-Za-z0-9_-]{1,64}$")
MAX_FRAMES = 600  # 10 seconds of game time per request is plenty


class Emulator:
    def __init__(self, rom: str, states_dir: Path):
        self.pyboy = PyBoy(rom, window="null", sound_emulated=False)
        self.pyboy.set_emulation_speed(0)  # 0 = as fast as the CPU allows
        self.states_dir = states_dir
        self.states_dir.mkdir(parents=True, exist_ok=True)
        self.frame = 0

    def tick(self, n: int):
        if n > 0:
            self.pyboy.tick(n)
            self.frame += n

    def press(self, buttons: list[str], hold: int, wait: int):
        for b in buttons:
            self.pyboy.button_press(b)
        self.tick(hold)
        for b in buttons:
            self.pyboy.button_release(b)
        self.tick(wait)

    def screen_png(self) -> bytes:
        buf = io.BytesIO()
        self.pyboy.screen.image.convert("RGB").save(buf, format="PNG")
        return buf.getvalue()

    def read(self, addr: int, length: int) -> bytes:
        return bytes(self.pyboy.memory[addr + i] for i in range(length))

    def save(self, slot: str):
        with open(self.states_dir / f"{slot}.state", "wb") as f:
            self.pyboy.save_state(f)

    def load(self, slot: str):
        with open(self.states_dir / f"{slot}.state", "rb") as f:
            self.pyboy.load_state(f)


class BadRequest(Exception):
    pass


def make_handler(emu: Emulator):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, fmt, *args):
            pass  # the Go side logs; keep this quiet

        def send(self, code: int, body: bytes, ctype: str):
            self.send_response(code)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def send_json(self, code: int, obj):
            self.send(code, json.dumps(obj).encode(), "application/json")

        def body_json(self) -> dict:
            n = int(self.headers.get("Content-Length") or 0)
            if n == 0:
                return {}
            try:
                return json.loads(self.rfile.read(n))
            except json.JSONDecodeError as e:
                raise BadRequest(f"invalid JSON: {e}")

        def do_GET(self):
            self.dispatch("GET")

        def do_POST(self):
            self.dispatch("POST")

        def dispatch(self, method: str):
            url = urlparse(self.path)
            route = ROUTES.get((method, url.path))
            if route is None:
                return self.send_json(404, {"error": f"no route {method} {url.path}"})
            try:
                route(self, url)
            except BadRequest as e:
                self.send_json(400, {"error": str(e)})
            except FileNotFoundError:
                self.send_json(404, {"error": "no save state with that slot name"})

        def health(self, url):
            self.send_json(200, {"ok": True, "frame": emu.frame, "title": emu.pyboy.cartridge_title})

        def press(self, url):
            req = self.body_json()
            buttons = req.get("buttons", [])
            hold, wait = int(req.get("hold", 8)), int(req.get("wait", 16))
            if not isinstance(buttons, list) or not set(buttons) <= BUTTONS:
                raise BadRequest(f"buttons must be a list drawn from {sorted(BUTTONS)}")
            if not (0 <= hold <= MAX_FRAMES and 0 <= wait <= MAX_FRAMES):
                raise BadRequest(f"hold and wait must be 0..{MAX_FRAMES}")
            emu.press(buttons, hold, wait)
            self.send_json(200, {"frame": emu.frame})

        def screen(self, url):
            self.send(200, emu.screen_png(), "image/png")

        def mem(self, url):
            q = parse_qs(url.query)
            try:
                addr = int(q["addr"][0], 0)  # accepts 0xD35E or 54110
                length = int(q.get("len", ["1"])[0])
            except (KeyError, ValueError):
                raise BadRequest("need ?addr=0x....&len=N")
            if not (0 <= addr <= 0xFFFF and 1 <= length <= 0x10000 - addr):
                raise BadRequest("address range out of bounds")
            self.send(200, emu.read(addr, length), "application/octet-stream")

        def slot(self) -> str:
            slot = self.body_json().get("slot", "")
            if not SLOT_RE.match(slot):
                raise BadRequest("slot must match [A-Za-z0-9_-]{1,64}")
            return slot

        def save(self, url):
            emu.save(self.slot())
            self.send_json(200, {"ok": True})

        def load(self, url):
            emu.load(self.slot())
            self.send_json(200, {"ok": True, "frame": emu.frame})

    ROUTES = {
        ("GET", "/health"): Handler.health,
        ("POST", "/press"): Handler.press,
        ("GET", "/screen.png"): Handler.screen,
        ("GET", "/mem"): Handler.mem,
        ("POST", "/state/save"): Handler.save,
        ("POST", "/state/load"): Handler.load,
    }
    return Handler


def play(rom: str):
    """Milestone 1: just play. Arrows, A key = A, S key = B, Enter = Start, Backspace = Select."""
    pyboy = PyBoy(rom, window="SDL2")
    while pyboy.tick():
        pass
    pyboy.stop()


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--rom", required=True)
    p.add_argument("--host", default="127.0.0.1")
    p.add_argument("--port", type=int, default=8765)
    p.add_argument("--states", default="states", help="directory for save states")
    p.add_argument("--play", action="store_true", help="open a window and play with the keyboard")
    args = p.parse_args()

    if args.play:
        return play(args.rom)

    emu = Emulator(args.rom, Path(args.states))
    # Single-threaded on purpose: the emulator is not thread-safe, and the
    # harness sends one request at a time anyway.
    server = HTTPServer((args.host, args.port), make_handler(emu))
    print(f"emulator server on http://{args.host}:{args.port} ({emu.pyboy.cartridge_title})")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        emu.pyboy.stop(save=False)


if __name__ == "__main__":
    main()
