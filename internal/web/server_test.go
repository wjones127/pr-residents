package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lancedb/pr-residents/internal/config"
	"github.com/lancedb/pr-residents/internal/jobs"
	"github.com/lancedb/pr-residents/internal/prr"
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
	srv, err := NewServer(st, &config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return srv, st
}

func seedRecord() *prr.Record {
	return &prr.Record{
		Repo: "o/r", Number: 5, URL: "https://x/5", Title: "Fix the bug", Lane: "fresh",
		Acuity:     prr.Acuity{Risk: "high", Urgency: "high", Rationale: "core change"},
		Effort:     prr.Effort{SizeBucket: "M"},
		MergeState: prr.MergeState{CI: "green"},
	}
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

// doRefresh with no configured repos writes an empty records list (no network).
func TestDoRefreshWritesEmptyWithoutRepos(t *testing.T) {
	srv, st := newTestServer(t, []*prr.Record{seedRecord()})
	if err := srv.doRefresh(func(jobs.Event) {}); err != nil {
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
