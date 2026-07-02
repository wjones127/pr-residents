// Package web serves the local rounds UI: the three-lane view rendered from the
// store, a Refresh button that runs the pipeline, and Server-Sent Events for
// live progress. Self-contained (inlined CSS/JS, no external assets).
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/lancedb/pr-residents/internal/agent"
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
	store      *store.FileStore
	cfg        *config.Config
	jobs       *jobs.Manager
	tmpl       *template.Template
	agent      agent.WorkupAgent
	newFetcher func(token string) agent.FileFetcher
	now        func() time.Time
}

// NewServer builds a Server. cfg is loaded once at startup (tokens are still
// read live from the environment at refresh/dispatch time).
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
	return &Server{
		store: st, cfg: cfg, jobs: jobs.New(), tmpl: tmpl,
		agent:      agent.NewClaudeAgent(),
		newFetcher: func(token string) agent.FileFetcher { return gh.NewClient(token) },
		now:        time.Now,
	}, nil
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /lanes", s.handleLanes)
	mux.HandleFunc("POST /refresh", s.handleRefresh)
	mux.HandleFunc("POST /dispatch", s.handleDispatch)
	mux.HandleFunc("POST /cancel", s.handleCancel)
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

// loadWorkups returns cached SOAPs keyed by "repo#number" for the records whose
// head SHA still matches (a moved head means the SOAP is stale, so it's skipped).
func (s *Server) loadWorkups(records []*prr.Record) map[string]Workup {
	out := map[string]Workup{}
	for _, r := range records {
		if r.HeadOid == "" {
			continue
		}
		var doc agent.WorkupDoc
		found, err := s.store.GetJSON(store.WorkupKey(r.Repo, r.Number, r.HeadOid), &doc)
		if err != nil || !found {
			continue
		}
		out[key(r.Repo, r.Number)] = Workup{
			SOAP: doc.SOAP, Recommendation: doc.Recommendation, BlockingCount: doc.BlockingCount,
		}
	}
	return out
}

func (s *Server) view() RoundsView {
	records := s.loadRecords()
	return BuildView(records, s.loadWorkups(records), today())
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "page", s.view()); err != nil {
		log.Printf("web: render page: %v", err)
	}
}

func (s *Server) handleLanes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "lanes", s.view()); err != nil {
		log.Printf("web: render lanes: %v", err)
	}
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.jobs.Run("refresh", s.doRefresh) {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, "refresh already running")
		return
	}
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprint(w, "refresh started")
}

func (s *Server) handleDispatch(w http.ResponseWriter, r *http.Request) {
	if !s.jobs.Run("dispatch", s.doDispatch) {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, "dispatch already running")
		return
	}
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprint(w, "dispatch started")
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	s.jobs.Cancel("dispatch")
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprint(w, "cancel requested")
}

// doDispatch runs review workups over the current records, streaming per-PR
// progress and running token totals. Cancel stops new work; finished SOAPs are
// already persisted.
func (s *Server) doDispatch(ctx context.Context, emit func(jobs.Event)) error {
	records := s.loadRecords()
	agent.Dispatch(ctx, s.cfg, s.store, s.agent, s.newFetcher, records, s.now().UTC(),
		func(ev agent.DispatchEvent) {
			emit(jobs.Event{
				Phase:     ev.Phase,
				Repo:      fmt.Sprintf("%s#%d", ev.Repo, ev.Number),
				Done:      ev.Done,
				Total:     ev.Total,
				TokensIn:  ev.TokensIn,
				TokensOut: ev.TokensOut,
				Message:   ev.Message,
				Status:    "running",
			})
		})
	return nil
}

// doRefresh runs the deterministic pipeline and persists records, emitting
// progress. Errors abort the job (surfaced as an SSE error event).
func (s *Server) doRefresh(ctx context.Context, emit func(jobs.Event)) error {
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
