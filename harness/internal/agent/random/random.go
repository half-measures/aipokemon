// Package random is the dumbest possible brain. It exists to prove the
// plumbing works before any tokens are spent.
package random

import (
	"context"
	"encoding/json"
	"math/rand/v2"

	"harness/internal/agent"
	"harness/internal/emulator"
)

// Weighted toward movement and A, which is what gets a player anywhere.
var choices = []string{
	emulator.Up, emulator.Down, emulator.Left, emulator.Right,
	emulator.Up, emulator.Down, emulator.Left, emulator.Right,
	emulator.A, emulator.A, emulator.B, emulator.Start,
}

type Agent struct {
	rng *rand.Rand
}

func New(seed uint64) *Agent {
	return &Agent{rng: rand.New(rand.NewPCG(seed, seed))}
}

func (a *Agent) Name() string { return "random" }

func (a *Agent) Decide(_ context.Context, _ agent.Observation) (agent.Action, error) {
	b := choices[a.rng.IntN(len(choices))]
	args, err := json.Marshal(agent.PressArgs{Buttons: []string{b}, Hold: 8, Wait: 16})
	if err != nil {
		return agent.Action{}, err
	}
	return agent.Action{Tool: "press", Args: args, Reasoning: "random " + b}, nil
}
