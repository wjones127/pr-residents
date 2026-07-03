// Package relevance is the pure scoring for pr-relevance: a path-affinity
// profile built from PRs I have reviewed, candidate scoring against it, and a
// cold-start fallback for when history is too thin to trust. No network, no LLM.
package relevance

import (
	"sort"
	"strconv"
	"strings"
)

const (
	// DefaultBucketDepth is the number of leading path segments an "area" key
	// keeps (a crate or top dir).
	DefaultBucketDepth = 2
	// MinHistory: below this many reviewed PRs in a repo, path-affinity is noise
	// and the caller should fall back to cold-start signals.
	MinHistory = 5
)

// BucketPath returns a coarse "area" key: the first depth path segments. A file
// at or near the root buckets by its directory (or the filename if at root).
func BucketPath(path string, depth int) string {
	var parts []string
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) <= depth {
		if len(parts) == 1 {
			return parts[0]
		}
		return strings.Join(parts[:len(parts)-1], "/")
	}
	return strings.Join(parts[:depth], "/")
}

// BucketsFor returns the set of area keys covered by paths.
func BucketsFor(paths []string, depth int) map[string]bool {
	out := map[string]bool{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		out[BucketPath(p, depth)] = true
	}
	return out
}

// PrimaryDomain returns the PR's dominant area: the bucket most changed files
// fall under, ties broken alphabetically. "" for an empty change set. This is
// the canonical domain for a disposition.
func PrimaryDomain(paths []string, depth int) string {
	counts := map[string]int{}
	for _, p := range paths {
		if b := BucketPath(p, depth); b != "" {
			counts[b]++
		}
	}
	if len(counts) == 0 {
		return ""
	}
	top := 0
	for _, c := range counts {
		if c > top {
			top = c
		}
	}
	var tied []string
	for b, c := range counts {
		if c == top {
			tied = append(tied, b)
		}
	}
	sort.Strings(tied)
	return tied[0]
}

// BuildProfile maps each area to how many of my reviewed PRs touched it.
// Counting PRs (not files) keeps one sprawling PR from dominating.
func BuildProfile(reviewedPathsPerPR [][]string, depth int) map[string]int {
	weights := map[string]int{}
	for _, paths := range reviewedPathsPerPR {
		for b := range BucketsFor(paths, depth) {
			weights[b]++
		}
	}
	return weights
}

// Match is one overlapping area and its profile weight.
type Match struct {
	Bucket string
	Weight int
}

// ScoreCandidate scores a candidate by overlap of its areas with the profile.
// matched is sorted by weight desc, then bucket asc.
func ScoreCandidate(candidatePaths []string, profile map[string]int, depth int) (float64, []Match) {
	var matched []Match
	for b := range BucketsFor(candidatePaths, depth) {
		if w, ok := profile[b]; ok {
			matched = append(matched, Match{Bucket: b, Weight: w})
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].Weight != matched[j].Weight {
			return matched[i].Weight > matched[j].Weight
		}
		return matched[i].Bucket < matched[j].Bucket
	})
	score := 0.0
	for _, m := range matched {
		score += float64(m.Weight)
	}
	return score, matched
}

// AffinityRationale renders a human-readable overlap summary.
func AffinityRationale(matched []Match) string {
	if len(matched) == 0 {
		return "no overlap with areas you've reviewed"
	}
	n := len(matched)
	if n > 3 {
		n = 3
	}
	parts := make([]string, 0, n)
	for _, m := range matched[:n] {
		parts = append(parts, m.Bucket+" (reviewed "+strconv.Itoa(m.Weight)+"×)")
	}
	return "overlaps your review history: " + strings.Join(parts, ", ")
}

// ColdStartScore ranks on the shared signals a new teammate already has:
// declared interests + hard-escalation paths.
func ColdStartScore(candidatePaths, interests, escalationRuleIDs []string) (float64, string) {
	matchedSet := map[string]bool{}
	for _, i := range interests {
		prefix := strings.TrimRight(i, "/") + "/"
		for _, p := range candidatePaths {
			if p == i || strings.HasPrefix(p, prefix) {
				matchedSet[i] = true
			}
		}
	}
	var matchedInterests []string
	for i := range matchedSet {
		matchedInterests = append(matchedInterests, i)
	}
	sort.Strings(matchedInterests)

	ruleIDs := append([]string{}, escalationRuleIDs...)
	score := float64(len(matchedInterests) + len(ruleIDs))

	var bits []string
	if len(matchedInterests) > 0 {
		bits = append(bits, "matches your interests: "+strings.Join(matchedInterests, ", "))
	}
	if len(ruleIDs) > 0 {
		bits = append(bits, "touches escalation paths: "+strings.Join(ruleIDs, ", "))
	}
	tail := "no shared-signal match"
	if len(bits) > 0 {
		tail = strings.Join(bits, "; ")
	}
	return score, "cold-start (thin history) — " + tail
}
