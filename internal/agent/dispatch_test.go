package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lancedb/pr-residents/internal/config"
	"github.com/lancedb/pr-residents/internal/gh"
	"github.com/lancedb/pr-residents/internal/prr"
	"github.com/lancedb/pr-residents/internal/store"
)

type fakeAgent struct {
	mu     sync.Mutex
	calls  int
	models []string
}

func (a *fakeAgent) Workup(ctx context.Context, p Packet, model string) (SOAP, error) {
	a.mu.Lock()
	a.calls++
	a.models = append(a.models, model)
	a.mu.Unlock()
	return SOAP{Text: "REVIEW " + p.PR.Repo, Recommendation: "approve", TokensIn: 10, TokensOut: 5}, nil
}

func rec(repo string, number int, lane, head string) *prr.Record {
	return &prr.Record{Repo: repo, Number: number, Lane: lane, HeadOid: head,
		Effort: prr.Effort{SizeBucket: "M"}}
}

func testCfg(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("GITHUB_TOKEN_O", "tok")
	return &config.Config{TokenPrefix: "GITHUB_TOKEN", Dispatch: config.Dispatch{Concurrency: 2}}
}

func fetcherFactory() func(string) FileFetcher {
	return func(string) FileFetcher {
		return fakeFetcher{files: []gh.FileDiff{{Filename: "a.go", Patch: "@@ -1 +1 @@\n-x\n+y", Additions: 1}}}
	}
}

func TestDispatchReviewsAndCaches(t *testing.T) {
	cfg := testCfg(t)
	st := store.New(t.TempDir())
	ag := &fakeAgent{}
	records := []*prr.Record{
		rec("o/r", 1, "fresh", "h1"),
		rec("o/r", 2, "re_review", "h2"),
		rec("o/r", 3, "housekeeping", "h3"), // not worked up
	}

	res := Dispatch(context.Background(), cfg, st, ag, fetcherFactory(), records, time.Now(), nil)
	if res.Reviewed != 2 || ag.calls != 2 {
		t.Fatalf("reviewed=%d calls=%d (housekeeping should be skipped)", res.Reviewed, ag.calls)
	}
	if res.TokensIn != 20 || res.TokensOut != 10 {
		t.Errorf("tokens: %d/%d", res.TokensIn, res.TokensOut)
	}
	// SOAP cached for the fresh PR at its head SHA.
	var doc WorkupDoc
	found, _ := st.GetJSON(store.WorkupKey("o/r", 1, "h1"), &doc)
	if !found || doc.SOAP == "" || doc.Recommendation != "approve" {
		t.Errorf("workup not cached: found=%v doc=%+v", found, doc)
	}
}

func TestDispatchReusesCache(t *testing.T) {
	cfg := testCfg(t)
	st := store.New(t.TempDir())
	records := []*prr.Record{rec("o/r", 1, "fresh", "h1")}

	Dispatch(context.Background(), cfg, st, &fakeAgent{}, fetcherFactory(), records, time.Now(), nil)

	second := &fakeAgent{}
	res := Dispatch(context.Background(), cfg, st, second, fetcherFactory(), records, time.Now(), nil)
	if res.Cached != 1 || res.Reviewed != 0 || second.calls != 0 {
		t.Errorf("second run should be all cached: %+v calls=%d", res, second.calls)
	}
}

func TestDispatchSkipsWithoutToken(t *testing.T) {
	cfg := &config.Config{TokenPrefix: "GITHUB_TOKEN", Dispatch: config.Dispatch{Concurrency: 2}}
	st := store.New(t.TempDir())
	ag := &fakeAgent{}
	records := []*prr.Record{rec("noauth/repo", 1, "fresh", "h1")}

	res := Dispatch(context.Background(), cfg, st, ag, fetcherFactory(), records, time.Now(), nil)
	if res.Skipped != 1 || res.Reviewed != 0 || ag.calls != 0 {
		t.Errorf("no-token PR should be skipped: %+v calls=%d", res, ag.calls)
	}
}

func TestDispatchCancelledDoesNoWork(t *testing.T) {
	cfg := testCfg(t)
	st := store.New(t.TempDir())
	ag := &fakeAgent{}
	records := []*prr.Record{rec("o/r", 1, "fresh", "h1"), rec("o/r", 2, "fresh", "h2")}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	res := Dispatch(ctx, cfg, st, ag, fetcherFactory(), records, time.Now(), nil)
	if res.Reviewed != 0 || ag.calls != 0 {
		t.Errorf("cancelled dispatch should review nothing: %+v calls=%d", res, ag.calls)
	}
}

func TestDispatchModelRouting(t *testing.T) {
	cfg := testCfg(t)
	cfg.Dispatch.ModelRouting = map[string]string{"fresh_m": "opus"}
	st := store.New(t.TempDir())
	ag := &fakeAgent{}

	Dispatch(context.Background(), cfg, st, ag, fetcherFactory(),
		[]*prr.Record{rec("o/r", 1, "fresh", "h1")}, time.Now(), nil)

	if len(ag.models) != 1 || ag.models[0] != "opus" {
		t.Errorf("expected model 'opus' from routing, got %+v", ag.models)
	}
}
