// Package runlog records every step of every run in SQLite, so runs can be
// replayed in the dashboard and compared later with plain SQL.
package runlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure Go: no cgo, builds on Windows as-is
)

const schema = `
CREATE TABLE IF NOT EXISTS runs (
	id         INTEGER PRIMARY KEY,
	started_at TEXT    NOT NULL,
	agent      TEXT    NOT NULL,
	game       TEXT    NOT NULL,
	seed       INTEGER NOT NULL,
	note       TEXT    NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS steps (
	run_id     INTEGER NOT NULL REFERENCES runs(id),
	step       INTEGER NOT NULL,
	at         TEXT    NOT NULL,
	screen     BLOB    NOT NULL, -- PNG, ~1-3 KB for a Game Boy frame
	state_json TEXT    NOT NULL,
	state_text TEXT    NOT NULL,
	tool       TEXT    NOT NULL,
	args       TEXT    NOT NULL,
	reasoning  TEXT    NOT NULL,
	think_ms   INTEGER NOT NULL,
	err        TEXT    NOT NULL,
	-- denormalised for cheap SQL over runs: progress by step, time per map
	map_name   TEXT    NOT NULL,
	x          INTEGER NOT NULL,
	y          INTEGER NOT NULL,
	PRIMARY KEY (run_id, step)
) WITHOUT ROWID;
`

type Run struct {
	ID        int64
	StartedAt time.Time
	Agent     string
	Game      string
	Seed      int64
	Steps     int // filled by ListRuns / GetRun
}

type Step struct {
	RunID     int64
	Step      int
	At        time.Time
	Screen    []byte
	StateJSON string
	StateText string
	Tool      string
	Args      string
	Reasoning string
	ThinkMS   int64
	Err       string
	MapName   string
	X, Y      int
}

var ErrNotFound = errors.New("not found")

type DB struct {
	db *sql.DB
}

func Open(path string) (*DB, error) {
	// WAL lets the dashboard read while the harness writes; busy_timeout
	// makes a reader wait briefly instead of failing on a lock.
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &DB{db: db}, nil
}

func (d *DB) Close() error { return d.db.Close() }

func (d *DB) StartRun(ctx context.Context, agent, game string, seed int64) (Run, error) {
	r := Run{StartedAt: time.Now().UTC(), Agent: agent, Game: game, Seed: seed}
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO runs (started_at, agent, game, seed) VALUES (?, ?, ?, ?)`,
		r.StartedAt.Format(time.RFC3339), agent, game, seed)
	if err != nil {
		return r, err
	}
	r.ID, err = res.LastInsertId()
	return r, err
}

func (d *DB) LogStep(ctx context.Context, s Step) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO steps (run_id, step, at, screen, state_json, state_text, tool, args,
		                   reasoning, think_ms, err, map_name, x, y)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.RunID, s.Step, s.At.UTC().Format(time.RFC3339Nano), s.Screen, s.StateJSON, s.StateText,
		s.Tool, s.Args, s.Reasoning, s.ThinkMS, s.Err, s.MapName, s.X, s.Y)
	return err
}

const runCols = `r.id, r.started_at, r.agent, r.game, r.seed,
	(SELECT COUNT(*) FROM steps s WHERE s.run_id = r.id)`

func (d *DB) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+runCols+` FROM runs r ORDER BY r.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (d *DB) GetRun(ctx context.Context, id int64) (Run, error) {
	return scanRun(d.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM runs r WHERE r.id = ?`, id))
}

type scanner interface{ Scan(...any) error }

func scanRun(sc scanner) (Run, error) {
	var r Run
	var started string
	if err := sc.Scan(&r.ID, &started, &r.Agent, &r.Game, &r.Seed, &r.Steps); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, ErrNotFound
		}
		return r, err
	}
	r.StartedAt, _ = time.Parse(time.RFC3339, started)
	return r, nil
}

// GetStep returns one step without its screenshot (see Screen).
func (d *DB) GetStep(ctx context.Context, runID int64, step int) (Step, error) {
	s := Step{RunID: runID, Step: step}
	var at string
	err := d.db.QueryRowContext(ctx, `
		SELECT at, state_json, state_text, tool, args, reasoning, think_ms, err, map_name, x, y
		FROM steps WHERE run_id = ? AND step = ?`, runID, step).
		Scan(&at, &s.StateJSON, &s.StateText, &s.Tool, &s.Args, &s.Reasoning, &s.ThinkMS, &s.Err, &s.MapName, &s.X, &s.Y)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	s.At, _ = time.Parse(time.RFC3339Nano, at)
	return s, err
}

func (d *DB) Screen(ctx context.Context, runID int64, step int) ([]byte, error) {
	var png []byte
	err := d.db.QueryRowContext(ctx, `SELECT screen FROM steps WHERE run_id = ? AND step = ?`, runID, step).Scan(&png)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return png, err
}
