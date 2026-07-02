package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lancedb/pr-residents/internal/agent"
	"github.com/lancedb/pr-residents/internal/config"
	"github.com/lancedb/pr-residents/internal/gh"
	"github.com/lancedb/pr-residents/internal/jobs"
	"github.com/lancedb/pr-residents/internal/prr"
	"github.com/lancedb/pr-residents/internal/relevance"
	"github.com/lancedb/pr-residents/internal/store"
)

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
	return agent.SOAP{Text: "REVIEW body here", Recommendation: "approve", BlockingCount: 0, TokensIn: 10, TokensOut: 5}, nil
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
	if !strings.Contains(body, "REVIEW body here") {
		t.Error("SOAP body not rendered after dispatch")
	}
	if !strings.Contains(body, "rec-approve") {
		t.Error("recommendation badge not rendered")
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
