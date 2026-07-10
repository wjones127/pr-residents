package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wjones127/pr-residents/internal/agent"
	"github.com/wjones127/pr-residents/internal/config"
	"github.com/wjones127/pr-residents/internal/gh"
	"github.com/wjones127/pr-residents/internal/jobs"
	"github.com/wjones127/pr-residents/internal/prr"
	"github.com/wjones127/pr-residents/internal/relevance"
	"github.com/wjones127/pr-residents/internal/store"
)

// waitJob blocks until an async job finishes, so its background writes complete
// before t.TempDir() cleanup runs.
func waitJob(t *testing.T, srv *Server, name string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for srv.jobs.Running(name) {
		select {
		case <-deadline:
			t.Fatalf("job %q did not finish", name)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func newTestServer(t *testing.T, records []*prr.Record) (*Server, *store.FileStore) {
	t.Helper()
	st := store.New(t.TempDir())
	if records != nil {
		if err := st.PutJSON(store.RecordsKey, records); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := NewServer(st, &config.Config{TokenPrefix: "GITHUB_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	return srv, st
}

func seedRecord() *prr.Record {
	return &prr.Record{
		Repo: "o/r", Number: 5, URL: "https://x/5", Title: "Fix the bug", Lane: "fresh",
		HeadOid:    "abc",
		Acuity:     prr.Acuity{Risk: "high", Urgency: "high", Rationale: "core change"},
		Effort:     prr.Effort{SizeBucket: "M"},
		MergeState: prr.MergeState{CI: "green"},
	}
}

type fakeAgent struct{}

func (fakeAgent) Workup(ctx context.Context, prompt string, model string) (agent.SOAP, error) {
	return agent.SOAP{
		Recommendation: "approve",
		Risk:           "med",
		Assessment:     "refined nil-deref risk",
		Summary:        "## Findings\n\nREVIEW body here\n\n- one item",
		Comments: []agent.DraftComment{
			{Path: "a.go", Line: 12, Side: "RIGHT", Label: "issue", Blocking: true, Body: "guard the nil case"},
		},
		TokensIn: 10, TokensOut: 5,
	}, nil
}

type fakeFetcher struct{}

func (fakeFetcher) PullFiles(owner, name string, number int) ([]gh.FileDiff, error) {
	return []gh.FileDiff{{Filename: "a.go", Patch: "@@ -1 +1 @@\n-x\n+y"}}, nil
}

func (fakeFetcher) ViewerLogin() (string, error) { return "me", nil }

func (fakeFetcher) Compare(owner, name, base, head string) (gh.CompareResult, error) {
	return gh.CompareResult{}, nil
}

func (fakeFetcher) FetchReReviewData(owner, name string, number int) (gh.ReReviewPR, error) {
	return gh.ReReviewPR{}, nil
}

func TestIndexRendersPage(t *testing.T) {
	srv, _ := newTestServer(t, []*prr.Record{seedRecord()})
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"<!doctype html>", "PR Review Rounds", "Fix the bug", "o/r#5", "Fresh"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestLanesFragment(t *testing.T) {
	srv, _ := newTestServer(t, []*prr.Record{seedRecord()})
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/lanes", nil))

	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Fix the bug") {
		t.Error("lanes fragment missing the PR")
	}
	if strings.Contains(body, "<!doctype html>") {
		t.Error("lanes fragment should not be a full page")
	}
}

func TestRefreshReturns202(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/refresh", nil))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202", rr.Code)
	}
	waitJob(t, srv, "refresh")
}

func TestTriageEndpointReturns202(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/triage", nil))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202", rr.Code)
	}
	waitJob(t, srv, "triage")
}

// doTriage with no configured repos writes an empty panel without touching the
// records list — the review lanes are refreshed by doRefresh independently.
func TestDoTriageWritesPanelOnly(t *testing.T) {
	srv, st := newTestServer(t, []*prr.Record{seedRecord()})
	if err := srv.doTriage(context.Background(), func(jobs.Event) {}); err != nil {
		t.Fatal(err)
	}
	var panel []relevance.Candidate
	found, err := st.GetJSON(store.PanelKey, &panel)
	if err != nil || !found {
		t.Fatalf("panel not written: found=%v err=%v", found, err)
	}

	// Records are left untouched by a triage refresh.
	var records []*prr.Record
	if _, err := st.GetJSON(store.RecordsKey, &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Errorf("expected records untouched (1), got %d", len(records))
	}
}

// doRefresh no longer builds the triage panel; that is doTriage's job.
func TestDoRefreshLeavesPanelUntouched(t *testing.T) {
	srv, st := newTestServer(t, nil)
	if err := st.PutJSON(store.PanelKey, []relevance.Candidate{
		{Repo: "o/r", Number: 7, Title: "Existing candidate"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.doRefresh(context.Background(), func(jobs.Event) {}); err != nil {
		t.Fatal(err)
	}
	var panel []relevance.Candidate
	if _, err := st.GetJSON(store.PanelKey, &panel); err != nil {
		t.Fatal(err)
	}
	if len(panel) != 1 {
		t.Errorf("expected panel untouched (1), got %d", len(panel))
	}
}

func TestTriagePanelRendered(t *testing.T) {
	srv, st := newTestServer(t, nil)
	if err := st.PutJSON(store.PanelKey, []relevance.Candidate{
		{Repo: "o/r", Number: 7, Title: "Nice candidate", URL: "u", Author: "alice", Score: 4.5, Rationale: "overlaps your history"},
	}); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rr.Body.String()
	for _, want := range []string{"Triage", "Nice candidate", "4.5", "overlaps your history", "o/r#7"} {
		if !strings.Contains(body, want) {
			t.Errorf("triage render missing %q", want)
		}
	}
}

func TestDispatchEndpointReturns202(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	srv.agent = fakeAgent{}
	srv.newFetcher = func(string) agent.Fetcher { return fakeFetcher{} }
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/dispatch", nil))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("dispatch status %d, want 202", rr.Code)
	}
	waitJob(t, srv, "dispatch")
}

func TestCancelEndpointReturns202(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/cancel", nil))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("cancel status %d, want 202", rr.Code)
	}
}

// doDispatch reviews the fresh PR and the cached SOAP then renders in its lane.
func TestDoDispatchCachesAndDisplaysSOAP(t *testing.T) {
	t.Setenv("GITHUB_TOKEN_O", "tok")
	srv, _ := newTestServer(t, []*prr.Record{seedRecord()})
	srv.agent = fakeAgent{}
	srv.newFetcher = func(string) agent.Fetcher { return fakeFetcher{} }

	if err := srv.doDispatch(context.Background(), func(jobs.Event) {}); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rr.Body.String()
	for _, want := range []string{
		"REVIEW body here",        // summary card
		"<h2>Findings</h2>",       // summary markdown rendered to HTML, not raw "##"
		"<li>one item</li>",       // list rendered
		"rec-approve",             // recommendation badge
		"guard the nil case",      // the draft comment body
		"/o/r/pull/5/files#diff-", // deep link to the exact line
		`class="copy-btn"`,        // a copy button
		"refined nil-deref risk",  // resident's refined rationale replaces the baseline on the row
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dispatch render missing %q", want)
		}
	}
	// The copy source keeps the raw markdown for pasting into GitHub.
	if !strings.Contains(body, "## Findings") {
		t.Error("copy source should retain raw markdown")
	}
}

// doRefresh with no configured repos writes an empty records list (no network).
func TestDoRefreshWritesEmptyWithoutRepos(t *testing.T) {
	srv, st := newTestServer(t, []*prr.Record{seedRecord()})
	if err := srv.doRefresh(context.Background(), func(jobs.Event) {}); err != nil {
		t.Fatal(err)
	}
	var records []*prr.Record
	found, err := st.GetJSON(store.RecordsKey, &records)
	if err != nil || !found {
		t.Fatalf("records not written: found=%v err=%v", found, err)
	}
	if len(records) != 0 {
		t.Errorf("expected empty records with no repos, got %d", len(records))
	}
}
