// Command dash serves the dashboard over an existing run log, for browsing
// past runs without the emulator running.
//
//	go run ./cmd/dash --db runs.db --addr :8080
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"

	"harness/internal/runlog"
	"harness/internal/web"
)

func main() {
	dbPath := flag.String("db", "runs.db", "SQLite run log")
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	db, err := runlog.Open(*dbPath)
	if err != nil {
		slog.Error("open run log", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	srv, err := web.NewServer(db, nil)
	if err != nil {
		slog.Error("dashboard", "err", err)
		os.Exit(1)
	}
	slog.Info("dashboard up", "url", "http://localhost"+*addr)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		slog.Error("dashboard stopped", "err", err)
		os.Exit(1)
	}
}
