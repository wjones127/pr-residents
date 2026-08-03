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
	"net/url"
	"strings"
	"time"

	"github.com/wjones127/pr-residents/internal/agent"
	"github.com/wjones127/pr-residents/internal/cache"
	"github.com/wjones127/pr-residents/internal/config"
	"github.com/wjones127/pr-residents/internal/gh"
	"github.com/wjones127/pr-residents/internal/jobs"
	"github.com/wjones127/pr-residents/internal/pipeline"
	"github.com/wjones127/pr-residents/internal/prr"
	"github.com/wjones127/pr-residents/internal/relevance"
	"github.com/wjones127/pr-residents/internal/store"
)

//go:embed templates/*
var templatesFS embed.FS

// ReviewPoster writes a pending review to GitHub, appending to the viewer's
// existing pending review when one exists. Abstracted so the /review handler can
// be tested without a live GitHub.
type ReviewPoster interface {
	PostPendingReview(owner, name string, number int, commitID string, comments []gh.ReviewComment) error
}

// Server holds the dependencies for the rounds UI.
type Server struct {
	store      *store.FileStore
	cfg        *config.Config
	jobs       *jobs.Manager
	tmpl       *template.Template
	agent      agent.WorkupAgent
	newFetcher func(token string) agent.Fetcher
	newPoster  func(token string) ReviewPoster
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
		newFetcher: func(token string) agent.Fetcher { return gh.NewClient(token) },
		newPoster:  func(token string) ReviewPoster { return gh.NewClient(token) },
		now:        time.Now,
	}, nil
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /lanes", s.handleLanes)
	mux.HandleFunc("POST /refresh", s.handleRefresh)
	mux.HandleFunc("POST /triage", s.handleTriage)
	mux.HandleFunc("POST /dispatch", s.handleDispatch)
	mux.HandleFunc("POST /review", s.handleReview)
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

// loadWorkups returns cached reviews keyed by "repo#number" for the records
// whose head SHA still matches (a moved head means the review is stale, skipped).
func (s *Server) loadWorkups(records []*prr.Record) map[string]agent.WorkupDoc {
	out := map[string]agent.WorkupDoc{}
	for _, r := range records {
		if r.HeadOid == "" {
			continue
		}
		var doc agent.WorkupDoc
		found, err := s.store.GetJSON(store.WorkupKey(r.Repo, r.Number, r.HeadOid), &doc)
		if err != nil || !found {
			continue
		}
		out[key(r.Repo, r.Number)] = doc
	}
	return out
}

func (s *Server) loadPanel() []relevance.Candidate {
	var panel []relevance.Candidate
	if _, err := s.store.GetJSON(store.PanelKey, &panel); err != nil {
		log.Printf("web: read panel: %v", err)
	}
	return panel
}

// reviewSearchQuery is the manual "find PRs to review" filter, mirrored from the
// old HTML render's per-repo links.
const reviewSearchQuery = "is:open is:pr draft:false review:required -author:@me"

func (s *Server) repoLinks() []RepoLink {
	var links []RepoLink
	for _, repo := range s.cfg.ActiveRepos() {
		links = append(links, RepoLink{
			Name: repo,
			URL:  "https://github.com/" + repo + "/pulls?q=" + url.QueryEscape(reviewSearchQuery),
		})
	}
	return links
}

func (s *Server) view() RoundsView {
	records := s.loadRecords()
	v := BuildView(records, s.loadWorkups(records), s.loadPanel(), today())
	v.RepoLinks = s.repoLinks()
	return v
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

func (s *Server) handleTriage(w http.ResponseWriter, r *http.Request) {
	if !s.jobs.Run("triage", s.doTriage) {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, "triage already running")
		return
	}
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprint(w, "triage started")
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

// reviewRequest is the POST /review body: the PR identity, the head SHA the
// workup was cached against, and the indexes of the comments to push.
type reviewRequest struct {
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	SHA      string `json:"sha"`
	Comments []int  `json:"comments"`
}

// handleReview pushes selected draft comments to GitHub as a single pending
// (unsubmitted) review. Comment bodies are rebuilt from the cached workup — not
// trusted from the client — and the SHA pins the review to the reviewed commit,
// so a stale (force-pushed) workup is rejected rather than posted against a
// moved head.
func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	writeErr := func(status int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
	}

	var req reviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(http.StatusBadRequest, "bad request body")
		return
	}
	owner, name, ok := strings.Cut(req.Repo, "/")
	if !ok || req.Number == 0 || req.SHA == "" {
		writeErr(http.StatusBadRequest, "repo, number and sha are required")
		return
	}

	var doc agent.WorkupDoc
	found, err := s.store.GetJSON(store.WorkupKey(req.Repo, req.Number, req.SHA), &doc)
	if err != nil {
		writeErr(http.StatusInternalServerError, "read workup: "+err.Error())
		return
	}
	if !found {
		writeErr(http.StatusConflict, "workup is stale — re-dispatch this PR")
		return
	}

	token := s.cfg.TokenFor(owner)
	if token == "" {
		writeErr(http.StatusBadRequest, "no token for "+owner)
		return
	}

	var comments []gh.ReviewComment
	for _, i := range req.Comments {
		if i < 0 || i >= len(doc.Comments) {
			continue
		}
		c := doc.Comments[i]
		if c.Path == "" {
			continue // review-level comments can't be posted inline
		}
		comments = append(comments, gh.ReviewComment{
			Path: c.Path, Line: c.Line, Side: c.Side, Body: copyText(c),
		})
	}
	if len(comments) == 0 {
		writeErr(http.StatusBadRequest, "select at least one inline comment")
		return
	}

	// Reject comments anchored outside the diff before posting. The agent
	// occasionally picks a line just past the last hunk (to "comment on the whole
	// file"); GitHub would 422 the entire review, so catch it here with the exact
	// offending anchors named.
	if bad, err := outOfDiff(s.newFetcher(token), owner, name, req.Number, comments); err != nil {
		writeErr(http.StatusBadGateway, "read diff: "+err.Error())
		return
	} else if len(bad) > 0 {
		writeErr(http.StatusUnprocessableEntity,
			"these comments anchor outside the diff — the agent overran the hunk; re-dispatch or edit the workup: "+strings.Join(bad, ", "))
		return
	}

	if err := s.newPoster(token).PostPendingReview(owner, name, req.Number, req.SHA, comments); err != nil {
		log.Printf("review: %s#%d: %v", req.Repo, req.Number, err)
		writeErr(http.StatusBadGateway, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":  true,
		"url": fmt.Sprintf("https://github.com/%s/pull/%d/files", req.Repo, req.Number),
	})
}

// outOfDiff fetches the PR's current diff and returns the "path:line (side)"
// anchors of any comment that falls outside a hunk — the lines GitHub would
// reject. An empty result means every comment is anchorable.
func outOfDiff(ff agent.Fetcher, owner, name string, number int, comments []gh.ReviewComment) ([]string, error) {
	files, err := ff.PullFiles(owner, name, number)
	if err != nil {
		return nil, err
	}
	right := map[string]map[int]bool{}
	left := map[string]map[int]bool{}
	for _, f := range files {
		right[f.Filename], left[f.Filename] = gh.CommentableLines(f.Patch)
	}
	var bad []string
	for _, c := range comments {
		set := right[c.Path]
		if c.Side == "LEFT" {
			set = left[c.Path]
		}
		if !set[c.Line] {
			bad = append(bad, fmt.Sprintf("%s:%d (%s)", c.Path, c.Line, c.Side))
		}
	}
	return bad, nil
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
	res := agent.Dispatch(ctx, s.cfg, s.store, s.agent, s.newFetcher, records, s.now().UTC(),
		func(ev agent.DispatchEvent) {
			// Surface per-PR problems in the server log — an SSE-only failure
			// leaves no trace to diagnose from.
			if ev.Phase == "failed" || ev.Phase == "cancelled" || ev.Phase == "skipped" {
				log.Printf("dispatch: %s#%d %s: %s", ev.Repo, ev.Number, ev.Phase, ev.Message)
			}
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
	log.Printf("dispatch complete: reviewed %d, cached %d, failed %d, skipped %d, tokens %d in / %d out",
		res.Reviewed, res.Cached, res.Failed, res.Skipped, res.TokensIn, res.TokensOut)
	// If nothing succeeded but work failed, surface it as a job error so the UI
	// shows it rather than a silent "done".
	if res.Reviewed == 0 && res.Failed > 0 {
		return fmt.Errorf("all %d review(s) failed — see server log for details", res.Failed)
	}
	return nil
}

// doRefresh runs the deterministic pipeline and persists records, emitting
// progress. Errors abort the job (surfaced as an SSE error event). The triage
// panel is refreshed separately (doTriage) so the review lanes can be updated
// without waiting on relevance ranking.
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

// doTriage rebuilds the triage panel (self-requested relevance candidates) and
// persists it. Independent of doRefresh so it can run on its own.
func (s *Server) doTriage(ctx context.Context, emit func(jobs.Event)) error {
	emit(jobs.Event{Phase: "triage", Message: "ranking candidates", Status: "running"})
	newRel := func(token string) relevance.API { return gh.NewClient(token) }
	panel, pwarns := relevance.BuildPanel(s.cfg, newRel, s.store, relevance.Options{})
	for _, warn := range pwarns {
		log.Println(warn)
	}
	if panel == nil {
		panel = []relevance.Candidate{}
	}
	return s.store.PutJSON(store.PanelKey, panel)
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
