// Package agent defines the brain contract. It is game-agnostic on purpose:
// a random bot, Claude, and a local model all implement Agent, and phase 2
// (Factorio) reuses it unchanged.
package agent

import (
	"context"
	"encoding/json"
)

// Observation is everything the brain gets to see for one step.
type Observation struct {
	Step      int
	Screen    []byte       // PNG
	State     any          // decoded game state, e.g. pokered.State
	StateText string       // the same state as a short text block for the prompt
	Recent    []StepRecord // last N steps, oldest first
	Notes     string       // the agent's own long-term memory
}

// Action is one tool call chosen by the brain.
type Action struct {
	Tool      string          `json:"tool"` // "press" for now; "navigate_to", "write_note" later
	Args      json.RawMessage `json:"args"`
	Reasoning string          `json:"reasoning"`
}

type StepRecord struct {
	Step   int
	Action Action
	Err    string // non-empty if executing the action failed
}

type Agent interface {
	Name() string
	Decide(ctx context.Context, obs Observation) (Action, error)
}

// PressArgs are the arguments of the "press" tool. Buttons are pressed one
// after another; Hold and Wait (frames) default to 8 and 16 when zero.
type PressArgs struct {
	Buttons []string `json:"buttons"`
	Hold    int      `json:"hold,omitempty"`
	Wait    int      `json:"wait,omitempty"`
}
