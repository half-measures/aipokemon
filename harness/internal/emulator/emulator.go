// Package emulator defines the contract between the harness and whatever
// runs the game. The rest of the harness only sees the Emulator interface,
// so PyBoy (Game Boy) can later sit beside an mGBA backend (GBA).
package emulator

import (
	"context"
	"image"
)

// Button names, matching what the emulator server accepts.
const (
	A      = "a"
	B      = "b"
	Start  = "start"
	Select = "select"
	Up     = "up"
	Down   = "down"
	Left   = "left"
	Right  = "right"
)

// Input holds Buttons down for Hold frames, releases them, then lets the
// game run Wait more frames so the result of the press is on screen.
type Input struct {
	Buttons []string `json:"buttons"`
	Hold    int      `json:"hold"`
	Wait    int      `json:"wait"`
}

type Emulator interface {
	Press(ctx context.Context, in Input) error
	Screen(ctx context.Context) (image.Image, error)
	ReadMem(ctx context.Context, addr uint16, n int) ([]byte, error)
	SaveState(ctx context.Context, slot string) error
	LoadState(ctx context.Context, slot string) error
}
