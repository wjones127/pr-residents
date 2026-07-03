// Package derive builds a PRRecord from raw GitHub detail (see docs/prrecord.md).
// Pure functions over the detail + viewer identity + escalation rules: no
// network, no LLM. This is the deterministic baseline residents later refine,
// and the highest-correctness-risk piece — it owns the traps in the contract.
package derive

import (
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lancedb/pr-residents/internal/gh"
	"github.com/lancedb/pr-residents/internal/prr"
)

// Version bumps when derivation logic changes in a way that should invalidate
// the cache. Kept in lock-step with the Python DERIVE_VERSION for parity.
const Version = "6"

const staleAuthorHrs = 48.0

// Changed-file path types treated as inherently low risk for the baseline.
var lowRiskPatterns = []string{
	"*.md", "*.txt", "*.rst", "docs/**", "**/docs/**",
	"**/test_*", "**/*_test.*", "**/tests/**", "*.lock",
	"**/Cargo.lock", "**/*.toml", "**/.github/**",
}

var ciMap = map[string]string{
	"SUCCESS":  "green",
	"FAILURE":  "red",
	"ERROR":    "red",
	"PENDING":  "pending",
	"EXPECTED": "pending",
}

// --- glob matching --------------------------------------------------------

var regexCache sync.Map // pattern -> *regexp.Regexp

// globToRegex mirrors the Python translation: `**/` matches zero or more leading
// path segments, `**` matches anything, `*` stays within a segment.
func globToRegex(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); {
		switch {
		case strings.HasPrefix(pattern[i:], "**/"):
			b.WriteString(`(?:.*/)?`)
			i += 3
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(`.*`)
			i += 2
		case pattern[i] == '*':
			b.WriteString(`[^/]*`)
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	b.WriteString("$")
	return b.String()
}

func compiled(pattern string) *regexp.Regexp {
	if v, ok := regexCache.Load(pattern); ok {
		return v.(*regexp.Regexp)
	}
	re := regexp.MustCompile(globToRegex(pattern))
	regexCache.Store(pattern, re)
	return re
}

// PathMatches reports whether path matches a glob pattern.
func PathMatches(path, pattern string) bool {
	return compiled(pattern).MatchString(path)
}

func anyPathMatches(paths, patterns []string) bool {
	for _, p := range paths {
		for _, pat := range patterns {
			if PathMatches(p, pat) {
				return true
			}
		}
	}
	return false
}

// --- helpers --------------------------------------------------------------

func parseTS(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// roundTo1 rounds to one decimal with ties-to-even, matching Python round().
func roundTo1(x float64) float64 {
	return math.RoundToEven(x*10) / 10
}

func hoursSince(value string, now time.Time) float64 {
	ts, ok := parseTS(value)
	if !ok {
		return 0.0
	}
	return roundTo1(now.Sub(ts).Seconds() / 3600.0)
}

func myLatestReview(reviews []gh.Review, viewer string) *gh.Review {
	var latest *gh.Review
	for i := range reviews {
		r := &reviews[i]
		if r.Author == nil || r.Author.Login != viewer || r.SubmittedAt == "" {
			continue
		}
		if latest == nil || r.SubmittedAt > latest.SubmittedAt {
			latest = r
		}
	}
	return latest
}

// headArrivedAt returns the latest of the head commit date and any force-push
// event (trap #1: ordering/identity, never pushedDate).
func headArrivedAt(d *gh.Detail) string {
	var times []string
	if len(d.Commits.Nodes) > 0 {
		if cd := d.Commits.Nodes[0].Commit.CommittedDate; cd != "" {
			times = append(times, cd)
		}
	}
	for _, n := range d.TimelineItems.Nodes {
		if n.Typename == "HeadRefForcePushedEvent" && n.CreatedAt != "" {
			times = append(times, n.CreatedAt)
		}
	}
	max := ""
	for _, t := range times {
		if t > max {
			max = t
		}
	}
	return max
}

// --- escalation -----------------------------------------------------------

// MatchEscalation matches a PR against the bright-line rules.
func MatchEscalation(paths, labels []string, totalLines int, rules prr.EscalationRules) prr.Escalation {
	ruleIDs := []string{}
	var reasons []string
	for _, rule := range rules.PathRules {
		if anyPathMatches(paths, rule.AnyPathMatches) {
			ruleIDs = append(ruleIDs, rule.ID)
			reasons = append(reasons, rule.Reason)
		}
	}
	labelSet := map[string]bool{}
	for _, l := range labels {
		labelSet[strings.ToLower(l)] = true
	}
	for _, rule := range rules.LabelRules {
		for _, l := range rule.AnyLabelMatches {
			if labelSet[strings.ToLower(l)] {
				ruleIDs = append(ruleIDs, rule.ID)
				reasons = append(reasons, rule.Reason)
				break
			}
		}
	}
	for _, rule := range rules.SizeRules {
		if totalLines >= rule.MinTotalLines {
			ruleIDs = append(ruleIDs, rule.ID)
			reasons = append(reasons, rule.Reason)
		}
	}
	return prr.Escalation{
		Forced:  len(ruleIDs) > 0,
		RuleIDs: ruleIDs,
		Reason:  strings.Join(reasons, "; "),
	}
}

// --- axis derivations -----------------------------------------------------

// DeriveEffort buckets by total changed lines (purely mechanical).
func DeriveEffort(additions, deletions, files int) prr.Effort {
	total := additions + deletions
	var bucket string
	switch {
	case total <= 10:
		bucket = "XS"
	case total <= 50:
		bucket = "S"
	case total <= 200:
		bucket = "M"
	case total <= 600:
		bucket = "L"
	default:
		bucket = "XL"
	}
	return prr.Effort{SizeBucket: bucket, Additions: additions, Deletions: deletions, Files: files}
}

// DeriveAuthorStatus maps prior merged-PR count to contributor familiarity.
// A nil count means the search failed -> "unknown", never guessed.
func DeriveAuthorStatus(mergedCount *int) string {
	if mergedCount == nil {
		return "unknown"
	}
	switch n := *mergedCount; {
	case n == 0:
		return "first_time"
	case n <= 3:
		return "infrequent"
	case n <= 10:
		return "regular"
	default:
		return "core"
	}
}

// DeriveBlockedOn returns (blocked_on, my latest review). Pure SHA-identity
// logic (trap #1).
func DeriveBlockedOn(d *gh.Detail, viewer string, requested bool) (string, *gh.Review) {
	head := d.HeadRefOid
	latest := myLatestReview(d.Reviews.Nodes, viewer)

	if d.IsDraft {
		// A draft is the author's court whatever the review history says:
		// route to the author path (housekeeping-if-stale, never fresh/re_review).
		return "author", latest
	}
	if latest == nil {
		if requested {
			return "me", nil
		}
		return "other_reviewer", nil
	}

	reviewedOid := ""
	if latest.Commit != nil {
		reviewedOid = latest.Commit.Oid
	}
	state := latest.State

	if reviewedOid != "" && reviewedOid != head {
		// Author pushed since my review -> re-review owed (covers the
		// stale-approval-reopened case too).
		return "me", latest
	}
	switch state {
	case "APPROVED":
		return "merge", latest
	case "CHANGES_REQUESTED":
		return "author", latest
	case "COMMENTED":
		if requested {
			return "me", latest
		}
		return "author", latest
	}
	return "other_reviewer", latest
}

// DeriveAcuity scores risk on the change itself, independent of escalation.
func DeriveAcuity(paths []string, blockedOn string, ageHrs float64, ci string) prr.Acuity {
	risk, rationale := "med", "core library change; resident to refine from diff content"
	if len(paths) > 0 {
		allLow := true
		for _, p := range paths {
			if !anyPathMatches([]string{p}, lowRiskPatterns) {
				allLow = false
				break
			}
		}
		if allLow {
			risk, rationale = "low", "docs/tests/config/deps only"
		}
	}

	var urgency string
	switch {
	case blockedOn == "merge" && ci == "red":
		urgency = "high"
	case blockedOn == "me" && ageHrs > 72:
		urgency = "high"
	case ageHrs > 48:
		urgency = "med"
	default:
		urgency = "low"
	}
	return prr.Acuity{Risk: risk, Urgency: urgency, Rationale: rationale}
}

// DeriveLane is purely blocked_on-driven. Escalation does NOT override it.
// Returns (lane, surfaced): surfaced=false means drop the PR.
func DeriveLane(blockedOn string, lastReviewedSHA *string, delta *prr.Delta, ageHrs float64) (string, bool) {
	switch blockedOn {
	case "me":
		if lastReviewedSHA != nil && *lastReviewedSHA != "" && delta != nil {
			return "re_review", true
		}
		return "fresh", true
	case "merge":
		return "housekeeping", true
	case "author":
		if ageHrs >= staleAuthorHrs {
			return "housekeeping", true
		}
		return "", false
	}
	return "", false // other_reviewer: not surfaced
}

// MergeStateFromDetail reads CI rollup + mergeability from a PR detail.
func MergeStateFromDetail(d *gh.Detail) prr.MergeState {
	ci := "pending"
	if len(d.Commits.Nodes) > 0 {
		if rollup := d.Commits.Nodes[0].Commit.StatusCheckRollup; rollup != nil {
			if v, ok := ciMap[rollup.State]; ok {
				ci = v
			}
		}
	}
	var mergeable *bool
	switch d.Mergeable {
	case "MERGEABLE":
		t := true
		mergeable = &t
	case "CONFLICTING":
		f := false
		mergeable = &f
	}
	return prr.MergeState{CI: ci, Mergeable: mergeable}
}

// --- top-level ------------------------------------------------------------

// BuildRecord builds a PRRecord, or nil if the PR should not be surfaced.
// The caller (sync) fills Repo and HeadOid afterward.
func BuildRecord(d *gh.Detail, viewer string, requested bool, rules prr.EscalationRules,
	now time.Time, authorMergedCount *int) *prr.Record {

	author := ""
	if d.Author != nil {
		author = d.Author.Login
	}
	if author == viewer {
		// Never surface my own PRs for review (inverted framing, out of scope).
		return nil
	}

	head := d.HeadRefOid
	files := make([]string, 0, len(d.Files.Nodes))
	for _, f := range d.Files.Nodes {
		files = append(files, f.Path)
	}
	labels := make([]string, 0, len(d.Labels.Nodes))
	for _, l := range d.Labels.Nodes {
		labels = append(labels, l.Name)
	}
	totalLines := d.Additions + d.Deletions

	escalation := MatchEscalation(files, labels, totalLines, rules)
	blockedOn, latest := DeriveBlockedOn(d, viewer, requested)

	var lastReviewed *string
	if latest != nil && latest.Commit != nil && latest.Commit.Oid != "" {
		s := latest.Commit.Oid
		lastReviewed = &s
	}
	var delta *prr.Delta
	if lastReviewed != nil && *lastReviewed != head {
		delta = &prr.Delta{Commits: []string{head}, FilesTouched: files}
	}

	// age_in_state: time since the event that put it in this state.
	var govTS string
	if (blockedOn == "author" || blockedOn == "merge") && latest != nil {
		govTS = latest.SubmittedAt
	} else {
		govTS = headArrivedAt(d)
	}
	ageHrs := hoursSince(govTS, now)

	ms := MergeStateFromDetail(d)

	lane, surfaced := DeriveLane(blockedOn, lastReviewed, delta, ageHrs)
	if !surfaced {
		return nil
	}

	return &prr.Record{
		Number:          d.Number,
		URL:             d.URL,
		Title:           d.Title,
		Body:            d.Body,
		Author:          author,
		AuthorStatus:    DeriveAuthorStatus(authorMergedCount),
		FilesChanged:    files,
		BlockedOn:       blockedOn,
		IsDraft:         d.IsDraft,
		AgeInStateHrs:   ageHrs,
		Lane:            lane,
		Acuity:          DeriveAcuity(files, blockedOn, ageHrs, ms.CI),
		Effort:          DeriveEffort(d.Additions, d.Deletions, d.ChangedFiles),
		Relevance:       prr.Relevance{Score: nil, Requested: requested},
		LastReviewedSHA: lastReviewed,
		Delta:           delta,
		Conditions:      []prr.Condition{},
		MergeState:      ms,
		Escalation:      escalation,
	}
}
