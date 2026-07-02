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
	"time"

	"github.com/lancedb/pr-residents/internal/cache"
	"github.com/lancedb/pr-residents/internal/config"
	"github.com/lancedb/pr-residents/internal/derive"
	"github.com/lancedb/pr-residents/internal/gh"
	"github.com/lancedb/pr-residents/internal/prr"
)

// API is the slice of the GitHub client that sync needs. *gh.Client satisfies
// it; tests supply a fake.
type API interface {
	ViewerLogin() (string, error)
	SearchLight(query string) ([]gh.LightPR, error)
	SearchCount(query string) (int, error)
	FetchDetail(owner, name string, number int) (*gh.Detail, error)
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

// Sync fetches and derives PRRecords for the config's active repos. newClient
// builds an API bound to a per-org token. Non-fatal problems are returned as
// warnings (a failed repo or PR is reported, never aborts the run).
func Sync(cfg *config.Config, newClient func(token string) API, c cache.Cache, now time.Time) ([]*prr.Record, []string) {
	var warns []string
	if err := c.EnsureFingerprint(Fingerprint(cfg.Escalation)); err != nil {
		warns = append(warns, fmt.Sprintf("[warn] cache fingerprint: %v", err))
	}

	clients := map[string]API{}
	viewers := map[string]string{}
	mergedCounts := map[string]int{} // "repo\x00author" -> merged PR count
	var records []*prr.Record

	for _, repo := range cfg.ActiveRepos() {
		owner, name := splitRepo(repo)
		token := cfg.TokenFor(owner)
		if token == "" {
			warns = append(warns, fmt.Sprintf("[skip] %s: $%s not set", repo, cfg.EnvVarFor(owner)))
			continue
		}
		if _, ok := clients[owner]; !ok {
			api := newClient(token)
			viewer, err := api.ViewerLogin()
			if err != nil {
				warns = append(warns, fmt.Sprintf("[error] %s: auth/viewer failed: %v", repo, err))
				continue
			}
			clients[owner] = api
			viewers[owner] = viewer
		}
		client := clients[owner]
		viewer := viewers[owner]

		// Light pass: which PRs are relevant, and which changed since last sync.
		light := map[int]gh.LightPR{}
		requested := map[int]bool{}
		failed := false
		for _, cat := range categories {
			hits, err := client.SearchLight(fmt.Sprintf("repo:%s is:open is:pr %s", repo, cat.qual))
			if err != nil {
				warns = append(warns, fmt.Sprintf("[error] %s: search failed: %v", repo, err))
				failed = true
				break
			}
			for _, h := range hits {
				light[h.Number] = h
				if cat.name == "requested" {
					requested[h.Number] = true
				}
			}
		}
		if failed {
			continue
		}

		for _, number := range sortedNumbers(light) {
			lr := light[number]
			req := requested[number]

			var record *prr.Record
			entry, err := c.Get(repo, number)
			if err != nil {
				warns = append(warns, fmt.Sprintf("[warn] %s#%d: cache read: %v", repo, number, err))
			}
			if entry != nil && entry.UpdatedAt == lr.UpdatedAt {
				record = entry.Record
				// `requested` can flip without updatedAt changing; refresh it.
				record.Relevance.Requested = req
			} else {
				detail, err := client.FetchDetail(owner, name, number)
				if err != nil {
					warns = append(warns, fmt.Sprintf("[error] %s#%d: detail failed: %v", repo, number, err))
					continue
				}
				if detail == nil {
					warns = append(warns, fmt.Sprintf("[warn] %s#%d: detail missing, skipped", repo, number))
					continue
				}
				mergedCount := authorMergedCount(client, repo, detail, viewer, mergedCounts, &warns)
				record = derive.BuildRecord(detail, viewer, req, cfg.Escalation, now, mergedCount)
				if record != nil {
					record.Repo = repo
					if err := c.Put(repo, number, lr.UpdatedAt, lr.HeadRefOid, record); err != nil {
						warns = append(warns, fmt.Sprintf("[warn] %s#%d: cache write: %v", repo, number, err))
					}
				}
			}
			if record != nil {
				// Current head SHA: the workup cache keys on it and it's how a
				// consumer detects the head moved without another fetch.
				record.HeadOid = lr.HeadRefOid
				records = append(records, record)
			}
		}
	}

	if err := c.Close(); err != nil {
		warns = append(warns, fmt.Sprintf("[warn] cache close: %v", err))
	}
	return records, warns
}

// authorMergedCount returns prior merged PRs by this author in this repo, for
// contributor status. Deduped per (repo, author). nil (-> "unknown") for my own
// PRs or if the search fails.
func authorMergedCount(client API, repo string, detail *gh.Detail, viewer string,
	counts map[string]int, warns *[]string) *int {

	author := ""
	if detail.Author != nil {
		author = detail.Author.Login
	}
	if author == "" || author == viewer {
		return nil
	}
	key := repo + "\x00" + author
	if v, ok := counts[key]; ok {
		return &v
	}
	n, err := client.SearchCount(fmt.Sprintf("repo:%s is:pr is:merged author:%s", repo, author))
	if err != nil {
		*warns = append(*warns, fmt.Sprintf("[warn] %s: merged-count for %s failed: %v", repo, author, err))
		return nil
	}
	counts[key] = n
	return &n
}

func sortedNumbers(m map[int]gh.LightPR) []int {
	out := make([]int, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}
