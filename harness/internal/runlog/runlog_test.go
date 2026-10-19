package runlog

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	run, err := db.StartRun(ctx, "random", "pokered", 7)
	if err != nil {
		t.Fatal(err)
	}
	in := Step{
		RunID: run.ID, Step: 1, At: time.Now(), Screen: []byte{0x89, 'P', 'N', 'G'},
		StateJSON: `{"map":"Pallet Town"}`, StateText: "Map: Pallet Town", Tool: "press",
		Args: `{"buttons":["a"]}`, Reasoning: "go", ThinkMS: 12, MapName: "Pallet Town", X: 5, Y: 6,
	}
	if err := db.LogStep(ctx, in); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetStep(ctx, run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tool != in.Tool || got.MapName != in.MapName || got.X != 5 || got.ThinkMS != 12 {
		t.Errorf("step = %+v", got)
	}
	png, err := db.Screen(ctx, run.ID, 1)
	if err != nil || string(png) != string(in.Screen) {
		t.Errorf("screen = %q, %v", png, err)
	}

	runs, err := db.ListRuns(ctx, 10)
	if err != nil || len(runs) != 1 || runs[0].Steps != 1 || runs[0].Agent != "random" {
		t.Errorf("runs = %+v, %v", runs, err)
	}

	if _, err := db.GetStep(ctx, run.ID, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing step err = %v, want ErrNotFound", err)
	}
}
