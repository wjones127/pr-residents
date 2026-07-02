package agent

import (
	"context"
	"sync"
	"time"

	"github.com/lancedb/pr-residents/internal/config"
	"github.com/lancedb/pr-residents/internal/prr"
	"github.com/lancedb/pr-residents/internal/store"
)

// WorkupDoc is a cached review, keyed by the PR's head SHA (a force-push
// invalidates it). Mirrors the Python workup shape: the human summary plus the
// structured draft comments the UI renders as anchored copy-cards.
type WorkupDoc struct {
	Repo           string         `json:"repo"`
	Number         int            `json:"number"`
	SHA            string         `json:"sha"`
	Recommendation string         `json:"recommendation"`
	Summary        string         `json:"summary"`
	Comments       []DraftComment `json:"comments"`
	CachedAt       string         `json:"cached_at"`
	TokensIn       int            `json:"tokens_in"`
	TokensOut      int            `json:"tokens_out"`
}

// DispatchEvent is a per-PR progress update. Done/Total track completed PRs;
// the token counts are running totals across the round.
type DispatchEvent struct {
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	Phase     string `json:"phase"` // reviewing | done | cached | failed | skipped | cancelled
	Done      int    `json:"done"`
	Total     int    `json:"total"`
	TokensIn  int    `json:"tokens_in"`
	TokensOut int    `json:"tokens_out"`
	Message   string `json:"message"`
}

// DispatchResult is the round summary.
type DispatchResult struct {
	Reviewed  int
	Cached    int
	Failed    int
	Skipped   int
	TokensIn  int
	TokensOut int
}

type workItem struct {
	rec     *prr.Record
	fetcher Fetcher
	viewer  string
	model   string
}

// Dispatch runs review workups over the fresh/re_review PRs in records, at
// cfg.Dispatch.Concurrency in parallel. A cached SOAP (matching the PR's head
// SHA) is reused and no agent runs. Each completed SOAP is persisted
// immediately, so cancelling ctx keeps finished reviews and discards only
// in-flight partials.
func Dispatch(ctx context.Context, cfg *config.Config, st *store.FileStore, ag WorkupAgent,
	newFetcher func(token string) Fetcher, records []*prr.Record, now time.Time,
	progress func(DispatchEvent)) DispatchResult {

	emit := func(DispatchEvent) {}
	if progress != nil {
		emit = progress
	}

	var needing []*prr.Record
	for _, r := range records {
		if r.Lane == "fresh" || r.Lane == "re_review" {
			needing = append(needing, r)
		}
	}
	total := len(needing)

	var mu sync.Mutex
	res := DispatchResult{}
	done := 0
	// finish records one PR as complete and emits an event under the lock.
	finish := func(r *prr.Record, phase, msg string) {
		mu.Lock()
		done++
		ev := DispatchEvent{
			Repo: r.Repo, Number: r.Number, Phase: phase, Done: done, Total: total,
			TokensIn: res.TokensIn, TokensOut: res.TokensOut, Message: msg,
		}
		mu.Unlock()
		emit(ev)
	}

	fetchers := map[string]Fetcher{}
	viewers := map[string]string{}
	skippedOwner := map[string]bool{}
	var work []workItem
	for _, r := range needing {
		var doc WorkupDoc
		if found, _ := st.GetJSON(store.WorkupKey(r.Repo, r.Number, r.HeadOid), &doc); found {
			mu.Lock()
			res.Cached++
			mu.Unlock()
			finish(r, "cached", "reused cached SOAP")
			continue
		}
		owner, _ := splitRepo(r.Repo)
		ff, ok := fetchers[owner]
		if !ok {
			if skippedOwner[owner] {
				mu.Lock()
				res.Skipped++
				mu.Unlock()
				finish(r, "skipped", "owner "+owner+" unavailable")
				continue
			}
			token := cfg.TokenFor(owner)
			if token == "" {
				skippedOwner[owner] = true
				mu.Lock()
				res.Skipped++
				mu.Unlock()
				finish(r, "skipped", "no token for "+owner)
				continue
			}
			ff = newFetcher(token)
			viewer, err := ff.ViewerLogin()
			if err != nil {
				skippedOwner[owner] = true
				mu.Lock()
				res.Skipped++
				mu.Unlock()
				finish(r, "skipped", "viewer failed for "+owner+": "+err.Error())
				continue
			}
			fetchers[owner] = ff
			viewers[owner] = viewer
		}
		work = append(work, workItem{rec: r, fetcher: ff, viewer: viewers[owner], model: ModelFor(cfg.Dispatch, r)})
	}

	workers := cfg.Dispatch.Concurrency
	if workers <= 0 {
		workers = 6
	}
	jobs := make(chan workItem)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range jobs {
				if ctx.Err() != nil {
					finish(it.rec, "cancelled", "cancelled before start")
					continue
				}
				mu.Lock()
				snap := DispatchEvent{Repo: it.rec.Repo, Number: it.rec.Number, Phase: "reviewing",
					Done: done, Total: total, TokensIn: res.TokensIn, TokensOut: res.TokensOut}
				mu.Unlock()
				emit(snap)

				soap, err := reviewOne(ctx, ag, st, now, it)
				if err != nil {
					phase := "failed"
					if ctx.Err() != nil {
						phase = "cancelled"
					}
					finish(it.rec, phase, err.Error())
					continue
				}
				mu.Lock()
				res.Reviewed++
				res.TokensIn += soap.TokensIn
				res.TokensOut += soap.TokensOut
				mu.Unlock()
				finish(it.rec, "done", soap.Recommendation)
			}
		}()
	}
	for _, it := range work {
		if ctx.Err() != nil {
			break // stop enqueuing on cancel; already-cached/completed persist
		}
		jobs <- it
	}
	close(jobs)
	wg.Wait()
	return res
}

// reviewOne builds the packet, runs the agent, and persists the SOAP to the
// workup cache (keyed on head SHA) before returning.
func reviewOne(ctx context.Context, ag WorkupAgent, st *store.FileStore, now time.Time, it workItem) (SOAP, error) {
	var prompt string
	if it.rec.Lane == "re_review" {
		pkt, err := BuildReReviewPacket(it.fetcher, it.rec, it.viewer)
		if err != nil {
			return SOAP{}, err
		}
		prompt = reReviewPrompt(pkt)
	} else {
		pkt, err := BuildPacket(it.fetcher, it.rec)
		if err != nil {
			return SOAP{}, err
		}
		prompt = freshPrompt(pkt)
	}
	soap, err := ag.Workup(ctx, prompt, it.model)
	if err != nil {
		return SOAP{}, err
	}
	doc := WorkupDoc{
		Repo: it.rec.Repo, Number: it.rec.Number, SHA: it.rec.HeadOid,
		Recommendation: soap.Recommendation, Summary: soap.Summary, Comments: soap.Comments,
		CachedAt: now.Format(time.RFC3339), TokensIn: soap.TokensIn, TokensOut: soap.TokensOut,
	}
	if err := st.PutJSON(store.WorkupKey(it.rec.Repo, it.rec.Number, it.rec.HeadOid), doc); err != nil {
		return soap, nil // review succeeded; a cache-write failure isn't fatal to the round
	}
	return soap, nil
}
