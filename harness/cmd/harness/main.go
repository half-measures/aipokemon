// Command harness runs the observe -> decide -> act loop against the
// emulator server.
//
//	go run ./cmd/harness --agent=random --steps=1000 --web :8080
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"harness/internal/agent"
	"harness/internal/agent/openaicompat"
	"harness/internal/agent/random"
	"harness/internal/emulator"
	"harness/internal/game/pokered"
	"harness/internal/runlog"
	"harness/internal/web"
)

const (
	recentWindow    = 20
	maxDecideErrors = 5
)

func main() {
	emuURL := flag.String("emu", "http://127.0.0.1:8765", "emulator server URL")
	agentName := flag.String("agent", "random", "brain to use: random, local")
	llmURL := flag.String("llm-url", "http://localhost:1234/v1", "OpenAI-compatible base URL (LM Studio :1234, Ollama :11434)")
	model := flag.String("model", "qwen/qwen3.5-9b", "model name as the local server lists it at /v1/models")
	vision := flag.Bool("vision", false, "also send the screenshot (needs a vision model)")
	autoDialog := flag.Bool("auto-dialog", true, "press a automatically when only a text box is open")
	steps := flag.Int("steps", 1000, "number of steps to run (0 = until Ctrl+C)")
	seed := flag.Uint64("seed", uint64(time.Now().UnixNano()), "seed for the random agent")
	loadState := flag.String("load-state", "", "save-state slot to start from")
	snapEvery := flag.Int("snap-every", 0, "write a screenshot every N steps (0 = never)")
	outDir := flag.String("out", "runs", "directory for screenshots")
	dbPath := flag.String("db", "runs.db", "SQLite file every step is logged to")
	webAddr := flag.String("web", "", "serve the live dashboard on this address, e.g. :8080")
	delay := flag.Duration("delay", 0, "pause between steps, e.g. 200ms, so fast agents are watchable")
	flag.Parse()

	// Ctrl+C cancels the context; the loop finishes its current step and exits.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, config{
		emuURL: *emuURL, agentName: *agentName, steps: *steps, seed: *seed,
		loadState: *loadState, snapEvery: *snapEvery, outDir: *outDir,
		dbPath: *dbPath, webAddr: *webAddr, delay: *delay, autoDialog: *autoDialog,
		llm: openaicompat.Config{BaseURL: *llmURL, Model: *model, Vision: *vision, APIKey: os.Getenv("LLM_API_KEY")},
	}); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("harness stopped", "err", err)
		os.Exit(1)
	}
}

type config struct {
	emuURL, agentName, loadState, outDir, dbPath, webAddr string
	steps, snapEvery                                      int
	seed                                                  uint64
	delay                                                 time.Duration
	llm                                                   openaicompat.Config
	autoDialog                                            bool
}

func run(ctx context.Context, cfg config) error {
	emu := emulator.NewHTTPClient(cfg.emuURL)
	h, err := emu.Health(ctx)
	if err != nil {
		return fmt.Errorf("emulator server not reachable (is server.py running?): %w", err)
	}
	slog.Info("connected", "title", h.Title, "frame", h.Frame)

	if cfg.loadState != "" {
		if err := emu.LoadState(ctx, cfg.loadState); err != nil {
			return err
		}
	}

	brain, err := newAgent(cfg)
	if err != nil {
		return err
	}

	db, err := runlog.Open(cfg.dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	run, err := db.StartRun(ctx, brain.Name(), "pokered", int64(cfg.seed))
	if err != nil {
		return err
	}
	slog.Info("run started", "run", run.ID, "db", cfg.dbPath)

	var broker *web.Broker
	if cfg.webAddr != "" {
		broker = web.NewBroker()
		srv, err := web.NewServer(db, broker)
		if err != nil {
			return err
		}
		go func() {
			if err := http.ListenAndServe(cfg.webAddr, srv.Handler()); err != nil {
				slog.Error("dashboard stopped", "err", err)
			}
		}()
		slog.Info("dashboard up", "url", fmt.Sprintf("http://localhost%s/runs/%d", cfg.webAddr, run.ID))
	}

	var runDir string
	if cfg.snapEvery > 0 {
		runDir = filepath.Join(cfg.outDir, time.Now().Format("20060102-150405"))
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			return err
		}
	}

	var recent []agent.StepRecord
	decideErrors := 0
	for step := 1; cfg.steps == 0 || step <= cfg.steps; step++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Observe.
		screen, err := screenPNG(ctx, emu)
		if err != nil {
			return err
		}
		state, err := pokered.Read(ctx, emu)
		if err != nil {
			return err
		}
		obs := agent.Observation{Step: step, Screen: screen, State: state, StateText: state.Summary(), Recent: recent}

		// Decide. Plain dialog only ever needs a, so the harness answers that
		// itself instead of spending a model call on it.
		start := time.Now()
		var act agent.Action
		if cfg.autoDialog && state.Screen.DialogOnly {
			act = agent.Action{Tool: "press", Args: json.RawMessage(`{"buttons":["a"]}`), Reasoning: "auto: advance dialog"}
		} else {
			act, err = brain.Decide(ctx, obs)
		}
		thinkTime := time.Since(start)
		if err != nil {
			// Models fail now and then (bad output, a timeout). Log it, show it
			// to the model next step, and only give up if it keeps happening.
			decideErrors++
			slog.Warn("decide failed", "step", step, "err", err, "in_a_row", decideErrors)
			if decideErrors >= maxDecideErrors {
				return fmt.Errorf("step %d: %d decide errors in a row, last: %w", step, decideErrors, err)
			}
			act = agent.Action{Tool: "none", Args: json.RawMessage(`{}`), Reasoning: "decide failed: " + err.Error()}
		} else {
			decideErrors = 0
		}

		// Act. A bad action is recorded and shown to the brain next step,
		// not fatal: models make mistakes and should see them.
		rec := agent.StepRecord{Step: step, Action: act}
		if act.Tool == "none" {
			rec.Err = act.Reasoning
		}
		if err := execute(ctx, emu, act); err != nil {
			rec.Err = err.Error()
		}
		recent = append(recent, rec)
		if len(recent) > recentWindow {
			recent = recent[1:]
		}

		stateJSON, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if err := db.LogStep(ctx, runlog.Step{
			RunID: run.ID, Step: step, At: time.Now(), Screen: screen,
			StateJSON: string(stateJSON), StateText: obs.StateText,
			Tool: act.Tool, Args: string(act.Args), Reasoning: act.Reasoning,
			ThinkMS: thinkTime.Milliseconds(), Err: rec.Err,
			MapName: state.MapName, X: state.X, Y: state.Y,
		}); err != nil {
			return fmt.Errorf("log step %d: %w", step, err)
		}
		if broker != nil {
			broker.Publish(web.StepEvent{RunID: run.ID, Step: step})
		}

		slog.Info("step", "n", step, "agent", brain.Name(), "tool", act.Tool,
			"args", string(act.Args), "think", thinkTime.Round(time.Millisecond), "err", rec.Err,
			"map", state.MapName, "x", state.X, "y", state.Y, "battle", state.Battle)

		if cfg.delay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(cfg.delay):
			}
		}

		if runDir != "" && step%cfg.snapEvery == 0 {
			if err := os.WriteFile(filepath.Join(runDir, fmt.Sprintf("step-%06d.png", step)), screen, 0o644); err != nil {
				return err
			}
		}
	}

	if cfg.webAddr != "" {
		slog.Info("run finished; dashboard still up, Ctrl+C to exit")
		<-ctx.Done()
	}
	return nil
}

func newAgent(cfg config) (agent.Agent, error) {
	switch cfg.agentName {
	case "random":
		return random.New(cfg.seed), nil
	case "local":
		return openaicompat.New(cfg.llm), nil
	default:
		return nil, fmt.Errorf("unknown agent %q", cfg.agentName)
	}
}

// execute runs one tool call against the emulator. This moves into the
// Pokemon game adapter in milestone 4, once there are game-specific tools.
func execute(ctx context.Context, emu emulator.Emulator, act agent.Action) error {
	switch act.Tool {
	case "none":
		return nil
	case "press":
		var a agent.PressArgs
		if err := json.Unmarshal(act.Args, &a); err != nil {
			return fmt.Errorf("press: bad args: %w", err)
		}
		hold, wait := cmp.Or(a.Hold, 8), cmp.Or(a.Wait, 16)
		for _, b := range a.Buttons {
			if err := emu.Press(ctx, emulator.Input{Buttons: []string{b}, Hold: hold, Wait: wait}); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown tool %q", act.Tool)
	}
}

func screenPNG(ctx context.Context, emu emulator.Emulator) ([]byte, error) {
	img, err := emu.Screen(ctx)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
