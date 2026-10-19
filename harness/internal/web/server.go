// Package web is the run dashboard: a run list, and a run viewer that
// follows the game live over server-sent events or scrubs back through
// past steps. HTML is rendered on the server; htmx swaps fragments in.
package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"harness/internal/runlog"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	db     *runlog.DB
	broker *Broker // nil when only browsing old runs
	tmpl   *template.Template
}

func NewServer(db *runlog.DB, broker *Broker) (*Server, error) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"pretty": prettyArgs,
	}).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{db: db, broker: broker, tmpl: tmpl}, nil
}

func (s *Server) Handler() http.Handler {
	static, _ := fs.Sub(staticFS, "static")
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", s.runs)
	mux.HandleFunc("GET /runs/{id}", s.run)
	mux.HandleFunc("GET /runs/{id}/panel", s.panel)
	mux.HandleFunc("GET /runs/{id}/steps/{step}/screen.png", s.screen)
	mux.HandleFunc("GET /runs/{id}/events", s.events)
	return mux
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	runs, err := s.db.ListRuns(r.Context(), 100)
	if err != nil {
		httpError(w, err)
		return
	}
	s.render(w, "runs.html", map[string]any{"Runs": runs})
}

// run renders the viewer page, opened on the latest step.
func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	run, err := s.db.GetRun(r.Context(), pathInt(r, "id"))
	if err != nil {
		httpError(w, err)
		return
	}
	data := map[string]any{"Run": run, "Live": s.broker != nil}
	if run.Steps > 0 {
		step, err := s.db.GetStep(r.Context(), run.ID, run.Steps)
		if err != nil {
			httpError(w, err)
			return
		}
		data["Step"] = step
	}
	s.render(w, "run.html", data)
}

// panel is the fragment the scrub slider fetches.
func (s *Server) panel(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("step"))
	step, err := s.db.GetStep(r.Context(), pathInt(r, "id"), n)
	if err != nil {
		httpError(w, err)
		return
	}
	s.render(w, "panel", step)
}

func (s *Server) screen(w http.ResponseWriter, r *http.Request) {
	png, err := s.db.Screen(r.Context(), pathInt(r, "id"), int(pathInt(r, "step")))
	if err != nil {
		httpError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // a step never changes
	w.Write(png)
}

// events streams one "step" SSE message per new step of this run. Each
// message is ready-to-swap HTML: the panel, plus an out-of-band slider
// update so the scrub range grows as the run does.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if s.broker == nil {
		http.Error(w, "not recording; nothing is live", http.StatusNotFound)
		return
	}
	runID := pathInt(r, "id")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher.Flush()

	ch := s.broker.Subscribe()
	defer s.broker.Unsubscribe(ch)
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if ev.RunID != runID {
				continue
			}
			step, err := s.db.GetStep(r.Context(), runID, ev.Step)
			if err != nil {
				slog.Warn("sse: load step", "err", err)
				continue
			}
			var buf bytes.Buffer
			if err := s.tmpl.ExecuteTemplate(&buf, "live", step); err != nil {
				slog.Warn("sse: render", "err", err)
				continue
			}
			// SSE frames are line-based: every line of the payload needs its own "data:" prefix.
			fmt.Fprint(w, "event: step\n")
			for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
				fmt.Fprintf(w, "data: %s\n", line)
			}
			fmt.Fprint(w, "\n")
			flusher.Flush()
		}
	}
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		httpError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func pathInt(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return n
}

func httpError(w http.ResponseWriter, err error) {
	if errors.Is(err, runlog.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	slog.Error("web", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// prettyArgs turns {"buttons":["a"],"hold":8,"wait":16} into "a (hold 8, wait 16)"
// for press actions, and leaves anything else as raw JSON.
func prettyArgs(tool, args string) string {
	if tool != "press" {
		return args
	}
	var a struct {
		Buttons    []string
		Hold, Wait int
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return args
	}
	return fmt.Sprintf("%s (hold %d, wait %d)", strings.Join(a.Buttons, "+"), a.Hold, a.Wait)
}
