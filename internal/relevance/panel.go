package relevance

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lancedb/pr-residents/internal/config"
	"github.com/lancedb/pr-residents/internal/derive"
	"github.com/lancedb/pr-residents/internal/gh"
	"github.com/lancedb/pr-residents/internal/store"
)

// API is the slice of the GitHub client relevance needs. *gh.Client satisfies it.
type API interface {
	ViewerLogin() (string, error)
	SearchWithFiles(query string, limit int) ([]gh.CandidatePR, error)
}

// Candidate is one scored triage candidate (a panel entry, persisted to PANEL).
type Candidate struct {
	Repo              string   `json:"repo"`
	Number            int      `json:"number"`
	Title             string   `json:"title"`
	URL               string   `json:"url"`
	Author            string   `json:"author"`
	Score             float64  `json:"score"`
	Mode              string   `json:"mode"` // affinity | cold_start
	Rationale         string   `json:"rationale"`
	MatchedAreas      []string `json:"matched_areas"`
	FilesChangedCount int      `json:"files_changed_count"`
}

// RepoProfile is the per-repo path-affinity profile learned from my reviews.
type RepoProfile struct {
	Reviews int            `json:"reviews"`
	Weights map[string]int `json:"weights"`
}

// profileBlob is the cached, viewer-scoped set of per-repo profiles.
type profileBlob struct {
	Viewer   string                 `json:"viewer"`
	Profiles map[string]RepoProfile `json:"profiles"`
}

// Options tunes a panel run. Zero values fall back to sensible defaults.
type Options struct {
	HistoryLimit   int
	CandidateLimit int
	Top            int
	MinScore       float64
	Rebuild        bool
}

func (o Options) withDefaults() Options {
	if o.HistoryLimit <= 0 {
		o.HistoryLimit = 100
	}
	if o.CandidateLimit <= 0 {
		o.CandidateLimit = 50
	}
	if o.Top <= 0 {
		o.Top = 10
	}
	if o.MinScore == 0 {
		o.MinScore = 1.0
	}
	return o
}

const botSuffix = "[bot]"

func isBot(login, typename string) bool {
	return typename == "Bot" || strings.HasSuffix(login, botSuffix)
}

// isClaimed reports whether another human has left a real review — someone else
// has it, so I'm not needed. Bots, my own reviews, and DISMISSED/PENDING don't
// count.
func isClaimed(reviews []gh.CandidateReview, viewer string) bool {
	for _, r := range reviews {
		if r.Login == "" || r.Login == viewer || isBot(r.Login, r.Type) {
			continue
		}
		if r.State == "DISMISSED" || r.State == "PENDING" {
			continue
		}
		return true
	}
	return false
}

func candidateQuery(repo string) string {
	return "repo:" + repo + " is:open is:pr draft:false -author:@me " +
		"-review-requested:@me -reviewed-by:@me sort:updated-desc"
}

func filtered(paths, exclude []string) []string {
	if len(exclude) == 0 {
		return paths
	}
	var out []string
	for _, p := range paths {
		drop := false
		for _, pat := range exclude {
			if derive.PathMatches(p, pat) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, p)
		}
	}
	return out
}

func splitOwner(repo string) string {
	if i := strings.IndexByte(repo, '/'); i >= 0 {
		return repo[:i]
	}
	return repo
}

// BuildPanel ranks self-requested triage candidates across the config's active
// repos. Path-affinity when review history is deep enough, cold-start signals
// otherwise. newClient builds an API bound to a per-org token. Non-fatal issues
// are returned as warnings.
func BuildPanel(cfg *config.Config, newClient func(token string) API, st *store.FileStore, opts Options) ([]Candidate, []string) {
	opts = opts.withDefaults()
	var warns []string

	clients := map[string]API{}
	var viewer string
	for _, repo := range cfg.ActiveRepos() {
		owner := splitOwner(repo)
		if _, ok := clients[owner]; ok {
			continue
		}
		token := cfg.TokenFor(owner)
		if token == "" {
			warns = append(warns, fmt.Sprintf("[skip] %s: $%s not set", repo, cfg.EnvVarFor(owner)))
			continue
		}
		api := newClient(token)
		if viewer == "" {
			v, err := api.ViewerLogin()
			if err != nil {
				warns = append(warns, fmt.Sprintf("[error] %s: viewer failed: %v", repo, err))
				continue
			}
			viewer = v
		}
		clients[owner] = api
	}
	if viewer == "" {
		return nil, warns
	}

	var repos []string
	for _, repo := range cfg.ActiveRepos() {
		if _, ok := clients[splitOwner(repo)]; ok {
			repos = append(repos, repo)
		}
	}

	profiles := loadProfiles(st, viewer)
	if profiles == nil {
		profiles = map[string]RepoProfile{}
	}
	// Build any active repo missing from the cached profile (so a newly-added
	// repo is back-filled, not stuck cold forever), or all repos on --rebuild.
	dirty := false
	for _, repo := range repos {
		if _, ok := profiles[repo]; ok && !opts.Rebuild {
			continue
		}
		api := clients[splitOwner(repo)]
		reviewed, err := api.SearchWithFiles("repo:"+repo+" is:pr reviewed-by:@me -author:@me", opts.HistoryLimit)
		if err != nil {
			warns = append(warns, fmt.Sprintf("[error] %s: history failed: %v", repo, err))
			continue
		}
		var pathsPerPR [][]string
		for _, pr := range reviewed {
			pathsPerPR = append(pathsPerPR, filtered(pr.Paths, cfg.ExcludePaths))
		}
		profiles[repo] = RepoProfile{Reviews: len(reviewed), Weights: BuildProfile(pathsPerPR, DefaultBucketDepth)}
		dirty = true
	}
	if dirty {
		saveProfiles(st, viewer, profiles)
	}

	var panel []Candidate
	for _, repo := range repos {
		api := clients[splitOwner(repo)]
		candidates, err := api.SearchWithFiles(candidateQuery(repo), opts.CandidateLimit)
		if err != nil {
			warns = append(warns, fmt.Sprintf("[error] %s: candidate search failed: %v", repo, err))
			continue
		}
		profile := profiles[repo]
		cold := profile.Reviews < MinHistory
		claimedCount, scored := 0, 0
		for _, c := range candidates {
			if isClaimed(c.Reviews, viewer) {
				claimedCount++
				continue
			}
			scored++
			paths := filtered(c.Paths, cfg.ExcludePaths)
			var score float64
			var rationale, mode string
			var matchedAreas []string
			if cold {
				esc := derive.MatchEscalation(paths, nil, 0, cfg.Escalation)
				score, rationale = ColdStartScore(paths, cfg.Interests, esc.RuleIDs)
				mode = "cold_start"
			} else {
				var matched []Match
				score, matched = ScoreCandidate(paths, profile.Weights, DefaultBucketDepth)
				rationale = AffinityRationale(matched)
				mode = "affinity"
				for _, m := range matched {
					matchedAreas = append(matchedAreas, m.Bucket)
				}
			}
			panel = append(panel, Candidate{
				Repo: c.Repo, Number: c.Number, Title: c.Title, URL: c.URL, Author: c.Author,
				Score: round2(score), Mode: mode, Rationale: rationale,
				MatchedAreas: matchedAreas, FilesChangedCount: len(paths),
			})
		}
		mode := "affinity"
		if cold {
			mode = "cold_start"
		}
		warns = append(warns, fmt.Sprintf("[info] %s: %d candidates, %d claimed-excluded, %d scored (mode=%s, history=%d)",
			repo, len(candidates), claimedCount, scored, mode, profile.Reviews))
	}

	var kept []Candidate
	for _, p := range panel {
		if p.Score >= opts.MinScore {
			kept = append(kept, p)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].Score != kept[j].Score {
			return kept[i].Score > kept[j].Score
		}
		if kept[i].Repo != kept[j].Repo {
			return kept[i].Repo < kept[j].Repo
		}
		return kept[i].Number < kept[j].Number
	})
	warns = append(warns, fmt.Sprintf("[info] triage: %d of %d scored candidates kept at min_score %.1f (interests configured: %d)",
		len(kept), len(panel), opts.MinScore, len(cfg.Interests)))
	if len(kept) > opts.Top {
		kept = kept[:opts.Top]
	}
	return kept, warns
}

func loadProfiles(st *store.FileStore, viewer string) map[string]RepoProfile {
	var blob profileBlob
	found, err := st.GetJSON(store.RelevanceProfileKey, &blob)
	if err != nil || !found || blob.Viewer != viewer { // never reuse another identity's profile
		return nil
	}
	return blob.Profiles
}

func saveProfiles(st *store.FileStore, viewer string, profiles map[string]RepoProfile) {
	_ = st.PutJSON(store.RelevanceProfileKey, profileBlob{Viewer: viewer, Profiles: profiles})
}

func round2(x float64) float64 {
	return float64(int64(x*100+0.5)) / 100
}
