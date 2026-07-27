// Package pipeline is the deterministic sync: fetch review-relevant PRs across
// the configured repos and emit PRRecords. No LLM. It owns the docs/prrecord.md
// correctness traps via the derive package, and skips the heavy detail query
// for PRs whose updatedAt has not changed (via the cache).
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wjones127/pr-residents/internal/cache"
	"github.com/wjones127/pr-residents/internal/config"
	"github.com/wjones127/pr-residents/internal/derive"
	"github.com/wjones127/pr-residents/internal/gh"
	"github.com/wjones127/pr-residents/internal/prr"
)

// API is the slice of the GitHub client that sync needs. *gh.Client satisfies
// it; tests supply a fake.
type API interface {
	ViewerLogin() (string, error)
	SearchLight(query string) ([]gh.LightPR, error)
	SearchCount(query string) (int, error)
	FetchDetail(owner, name string, number int) (*gh.Detail, []string, error)
}

// searchCategory pairs a relevance category with its search qualifier. `@me`
// resolves to the token owner; `-author:@me` excludes my own PRs.
type searchCategory struct {
	name string
	qual string
}

var categories = []searchCategory{
	{"requested", "review-requested:@me -author:@me"},
	{"reviewed", "reviewed-by:@me -author:@me"},
}

// Event is a sync progress update. Total is 0 when not yet known.
type Event struct {
	Phase string // "search" | "detail"
	Repo  string
	Done  int
	Total int
}

// ProgressFunc receives sync progress events (nil-safe: pass none to skip).
type ProgressFunc func(Event)

// Fingerprint is the cache-invalidation key: derivation inputs (escalation rules
// + logic version). It need not match the Python bytes — the Go cache is its own
// file — only change when the inputs change.
func Fingerprint(rules prr.EscalationRules) string {
	b, _ := json.Marshal(rules)
	sum := sha256.Sum256(append(b, derive.Version...))
	return hex.EncodeToString(sum[:])
}

func splitRepo(repo string) (owner, name string) {
	if i := strings.IndexByte(repo, '/'); i >= 0 {
		return repo[:i], repo[i+1:]
	}
	return repo, ""
}

// detailConcurrency bounds the parallel per-PR detail/merged-count fetches.
// Modest on purpose: GitHub's GraphQL secondary rate limits punish bursts, and
// this pool spans ALL repos, so it also caps total in-flight detail requests.
const detailConcurrency = 8

// mergedCountTTL is how long a cached author merged-count stays fresh. Merge
// history moves slowly, so a count that's stale by up to a week is fine and
// saves a search round-trip per author on nearly every refresh.
const mergedCountTTL = 7 * 24 * time.Hour

// repoWork is a repo's resolved client plus its searched PR set, carried from
// the search phase into the shared detail phase.
type repoWork struct {
	repo, owner, name, viewer string
	client                    API
	mergedCount               func(author string) *int
	light                     map[int]gh.LightPR
	requested                 map[int]bool
	numbers                   []int
}

// Sync fetches and derives PRRecords for the config's active repos. newClient
// builds an API bound to a per-org token. Non-fatal problems are returned as
// warnings (a failed repo or PR is reported, never aborts the run).
//
// Two phases, each parallel: (1) resolve per-owner clients and search every
// repo concurrently; (2) fan every PR across the configured repos through one
// shared detail worker pool. A single pool (rather than one per repo) keeps
// workers busy when repos are unevenly sized and bounds total in-flight
// requests. Output order is deterministic: repos in config order, PRs by number.
func Sync(cfg *config.Config, newClient func(token string) API, c cache.Cache, now time.Time, progress ...ProgressFunc) ([]*prr.Record, []string) {
	emit := func(Event) {}
	if len(progress) > 0 && progress[0] != nil {
		emit = progress[0]
	}

	var warns []string
	var warnsMu sync.Mutex
	warn := func(format string, args ...any) {
		warnsMu.Lock()
		warns = append(warns, fmt.Sprintf(format, args...))
		warnsMu.Unlock()
	}

	if err := c.EnsureFingerprint(Fingerprint(cfg.Escalation)); err != nil {
		warn("[warn] cache fingerprint: %v", err)
	}

	repos := cfg.ActiveRepos()
	clients, viewers := resolveClients(cfg, newClient, repos, warn)

	// Phase 1: search every repo concurrently. Results are indexed by repo order
	// so the assembled work list (and thus the output) stays deterministic.
	works := make([]*repoWork, len(repos))
	var searchDone int
	var searchMu sync.Mutex
	var swg sync.WaitGroup
	for repoIdx, repo := range repos {
		owner, name := splitRepo(repo)
		client := clients[owner]
		if client == nil {
			continue // no token / auth failed — already warned by resolveClients
		}
		swg.Add(1)
		go func(repoIdx int, repo, owner, name string) {
			defer swg.Done()
			light, requested, err := searchRepo(client, repo)
			searchMu.Lock()
			searchDone++
			emit(Event{Phase: "search", Repo: repo, Done: searchDone, Total: len(repos)})
			searchMu.Unlock()
			if err != nil {
				warn("[error] %s: search failed: %v", repo, err)
				return
			}
			works[repoIdx] = &repoWork{
				repo: repo, owner: owner, name: name, viewer: viewers[owner],
				client:      client,
				mergedCount: newMergedCounter(client, c, repo, viewers[owner], now, warn),
				light:       light, requested: requested, numbers: sortedNumbers(light),
			}
		}(repoIdx, repo, owner, name)
	}
	swg.Wait()

	// Barrier: flatten into one work list in (repo order, PR number) order.
	type item struct {
		rw     *repoWork
		number int
	}
	var work []item
	for _, rw := range works {
		if rw == nil {
			continue
		}
		for _, n := range rw.numbers {
			work = append(work, item{rw: rw, number: n})
		}
	}

	// Phase 2: one shared pool over every PR, with a single global progress
	// counter. out is index-keyed, so completion order does not affect output.
	out := make([]*prr.Record, len(work))
	var done int
	var mu sync.Mutex
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < detailConcurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				rec := buildRecord(c, cfg, now, work[i].rw, work[i].number, warn)
				mu.Lock()
				out[i] = rec
				done++
				emit(Event{Phase: "detail", Done: done, Total: len(work)})
				mu.Unlock()
			}
		}()
	}
	for i := range work {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	var records []*prr.Record
	for _, rec := range out {
		if rec != nil {
			records = append(records, rec)
		}
	}

	if err := c.Close(); err != nil {
		warn("[warn] cache close: %v", err)
	}
	return records, warns
}

// resolveClients builds one authenticated client per unique owner across repos,
// in parallel (ViewerLogin is a round-trip). A missing token or failed auth
// leaves the owner absent from the maps and is warned once; callers skip repos
// whose owner has no client.
func resolveClients(cfg *config.Config, newClient func(token string) API, repos []string,
	warn func(string, ...any)) (map[string]API, map[string]string) {

	tokens := map[string]string{}
	for _, repo := range repos {
		owner, _ := splitRepo(repo)
		if _, seen := tokens[owner]; seen {
			continue
		}
		token := cfg.TokenFor(owner)
		if token == "" {
			warn("[skip] %s: $%s not set", repo, cfg.EnvVarFor(owner))
			continue
		}
		tokens[owner] = token
	}

	clients := map[string]API{}
	viewers := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for owner, token := range tokens {
		wg.Add(1)
		go func(owner, token string) {
			defer wg.Done()
			api := newClient(token)
			viewer, err := api.ViewerLogin()
			if err != nil {
				warn("[error] %s: auth/viewer failed: %v", owner, err)
				return
			}
			mu.Lock()
			clients[owner] = api
			viewers[owner] = viewer
			mu.Unlock()
		}(owner, token)
	}
	wg.Wait()
	return clients, viewers
}

// buildRecord turns one PR into its record: a cache hit when unchanged, else a
// detail fetch + derive. Returns nil to skip (fetch failed / dropped). Safe to
// call concurrently — all shared state is behind the cache and warn closure.
func buildRecord(c cache.Cache, cfg *config.Config, now time.Time, rw *repoWork, number int,
	warn func(string, ...any)) *prr.Record {

	lr := rw.light[number]
	req := rw.requested[number]

	entry, err := c.Get(rw.repo, number)
	if err != nil {
		warn("[warn] %s#%d: cache read: %v", rw.repo, number, err)
	}
	// Reuse the cached record only when the PR is unchanged AND its
	// requested-status is the same. `requested` flipping (e.g. you just added
	// yourself as a reviewer) changes blocked_on/lane, which the cached record
	// was NOT derived with — so re-fetch and re-derive.
	if entry != nil && entry.UpdatedAt == lr.UpdatedAt &&
		entry.Record != nil && entry.Record.Relevance.Requested == req {
		rec := entry.Record
		rec.HeadOid = lr.HeadRefOid
		return rec
	}

	detail, dwarns, err := rw.client.FetchDetail(rw.owner, rw.name, number)
	if err != nil {
		warn("[error] %s#%d: detail failed: %v", rw.repo, number, err)
		return nil
	}
	for _, dw := range dwarns {
		warn("[warn] %s#%d: %s", rw.repo, number, dw)
	}
	if detail == nil {
		warn("[warn] %s#%d: detail missing, skipped", rw.repo, number)
		return nil
	}
	rec := derive.BuildRecord(detail, rw.viewer, req, cfg.Escalation, now, rw.mergedCount(authorLogin(detail)))
	if rec == nil {
		return nil
	}
	rec.Repo = rw.repo
	if err := c.Put(rw.repo, number, lr.UpdatedAt, lr.HeadRefOid, rec); err != nil {
		warn("[warn] %s#%d: cache write: %v", rw.repo, number, err)
	}
	// Current head SHA: the workup cache keys on it and it's how a consumer
	// detects the head moved without another fetch.
	rec.HeadOid = lr.HeadRefOid
	return rec
}

// searchRepo runs the relevance searches for a repo. The two categories are
// independent, so they fire concurrently. An error in either fails the repo
// (matching the serial behaviour: a partial search set is not trustworthy).
func searchRepo(client API, repo string) (map[int]gh.LightPR, map[int]bool, error) {
	type result struct {
		cat  string
		hits []gh.LightPR
		err  error
	}
	ch := make(chan result, len(categories))
	for _, cat := range categories {
		go func(cat searchCategory) {
			hits, err := client.SearchLight(fmt.Sprintf("repo:%s is:open is:pr %s", repo, cat.qual))
			ch <- result{cat: cat.name, hits: hits, err: err}
		}(cat)
	}
	light := map[int]gh.LightPR{}
	requested := map[int]bool{}
	var firstErr error
	for range categories {
		r := <-ch
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		for _, h := range r.hits {
			light[h.Number] = h
			if r.cat == "requested" {
				requested[h.Number] = true
			}
		}
	}
	if firstErr != nil {
		return nil, nil, firstErr
	}
	return light, requested, nil
}

// newMergedCounter returns a concurrency-safe lookup for an author's prior
// merged PRs in a repo (contributor status). It reads through the persistent
// cache with a TTL and memoizes within the run; nil (-> "unknown") for my own
// PRs or on failure. A rare concurrent miss may fetch the same author twice —
// idempotent and cheap, so not worth a singleflight.
func newMergedCounter(client API, c cache.Cache, repo, viewer string, now time.Time,
	warn func(string, ...any)) func(author string) *int {

	var mu sync.Mutex
	memo := map[string]*int{}

	return func(author string) *int {
		if author == "" || author == viewer {
			return nil
		}
		mu.Lock()
		if v, ok := memo[author]; ok {
			mu.Unlock()
			return v
		}
		mu.Unlock()

		if count, fetchedAt, ok, err := c.GetMergedCount(repo, author); err != nil {
			warn("[warn] %s: merged-count cache read for %s: %v", repo, author, err)
		} else if ok && now.Sub(fetchedAt) < mergedCountTTL {
			mu.Lock()
			memo[author] = &count
			mu.Unlock()
			return &count
		}

		n, err := client.SearchCount(fmt.Sprintf("repo:%s is:pr is:merged author:%s", repo, author))
		if err != nil {
			warn("[warn] %s: merged-count for %s failed: %v", repo, author, err)
			return nil
		}
		if err := c.PutMergedCount(repo, author, n, now); err != nil {
			warn("[warn] %s: merged-count cache write for %s: %v", repo, author, err)
		}
		mu.Lock()
		memo[author] = &n
		mu.Unlock()
		return &n
	}
}

// authorLogin is the PR author's login, or "" when unknown.
func authorLogin(d *gh.Detail) string {
	if d.Author != nil {
		return d.Author.Login
	}
	return ""
}

func sortedNumbers(m map[int]gh.LightPR) []int {
	out := make([]int, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}
