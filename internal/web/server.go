// Package web serves the local rounds UI: the three-lane view rendered from the
// store, a Refresh button that runs the pipeline, and Server-Sent Events for
// live progress. Self-contained (inlined CSS/JS, no external assets).
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/lancedb/pr-residents/internal/cache"
	"github.com/lancedb/pr-residents/internal/config"
	"github.com/lancedb/pr-residents/internal/gh"
	"github.com/lancedb/pr-residents/internal/jobs"
	"github.com/lancedb/pr-residents/internal/pipeline"
	"github.com/lancedb/pr-residents/internal/prr"
	"github.com/lancedb/pr-residents/internal/store"
)

//go:embed templates/*
var templatesFS embed.FS

// Server holds the dependencies for the rounds UI.
type Server struct {
	store *store.FileStore
	cfg   *config.Config
	jobs  *jobs.Manager
	tmpl  *template.Template
}

// NewServer builds a Server. cfg is loaded once at startup (tokens are still
// read live from the environment at refresh time).
func NewServer(st *store.FileStore, cfg *config.Config) (*Server, error) {
	css, err := templatesFS.ReadFile("templates/styles.css")
	if err != nil {
		return nil, err
	}
	js, err := templatesFS.ReadFile("templates/app.js")
	if err != nil {
		return nil, err
	}
	funcs := template.FuncMap{
		"css": func() template.CSS { return template.CSS(css) },
		"js":  func() template.JS { return template.JS(js) },
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(templatesFS, "templates/*.html.tmpl")
	if err != nil {
		return nil, err
	}
	return &Server{store: st, cfg: cfg, jobs: jobs.New(), tmpl: tmpl}, nil
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /lanes", s.handleLanes)
	mux.HandleFunc("POST /refresh", s.handleRefresh)
	mux.HandleFunc("GET /events", s.handleEvents)
	return mux
}

func (s *Server) loadRecords() []*prr.Record {
	var records []*prr.Record
	if _, err := s.store.GetJSON(store.RecordsKey, &records); err != nil {
		log.Printf("web: read records: %v", err)
	}
	if records == nil {
		records = []*prr.Record{}
	}
	return records
}

func today() string { return time.Now().UTC().Format("2006-01-02") }

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	view := BuildView(s.loadRecords(), today())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "page", view); err != nil {
		log.Printf("web: render page: %v", err)
	}
}

func (s *Server) handleLanes(w http.ResponseWriter, r *http.Request) {
	view := BuildView(s.loadRecords(), today())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "lanes", view); err != nil {
		log.Printf("web: render lanes: %v", err)
	}
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	started := s.jobs.Run("refresh", s.doRefresh)
	if !started {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, "refresh already running")
		return
	}
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprint(w, "refresh started")
}

// doRefresh runs the deterministic pipeline and persists records, emitting
// progress. Errors abort the job (surfaced as an SSE error event).
func (s *Server) doRefresh(emit func(jobs.Event)) error {
	dbPath, err := s.store.LocalPath(store.PRDetailDBKey, true)
	if err != nil {
		return err
	}
	c, err := cache.OpenSQLite(dbPath)
	if err != nil {
		return err
	}
	newClient := func(token string) pipeline.API { return gh.NewClient(token) }
	records, warns := pipeline.Sync(s.cfg, newClient, c, time.Now().UTC(), func(ev pipeline.Event) {
		emit(jobs.Event{Phase: ev.Phase, Repo: ev.Repo, Done: ev.Done, Total: ev.Total, Status: "running"})
	})
	for _, warn := range warns {
		log.Println(warn)
	}
	if records == nil {
		records = []*prr.Record{}
	}
	return s.store.PutJSON(store.RecordsKey, records)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	events, unsubscribe := s.jobs.Subscribe()
	defer unsubscribe()

	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
