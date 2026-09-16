// Package agent builds review packets, runs the review agent (headless Claude
// Code first), and orchestrates a dispatch round. The deterministic packet is
// built here in Go; the agent only exercises judgment over it and returns a SOAP.
package agent

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/wjones127/pr-residents/internal/config"
	"github.com/wjones127/pr-residents/internal/gh"
	"github.com/wjones127/pr-residents/internal/prr"
)

// maxPatchLines is the per-file soft cap on patch size. Real source diffs go
// through whole up to this; only pathological files (huge generated blobs that
// slipped past classification) get hunk-elided.
const maxPatchLines = 1500

// PacketPR is the PR identity + triage metadata the resident needs.
type PacketPR struct {
	Repo         string `json:"repo"`
	Number       int    `json:"number"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	Head         string `json:"head"`
	Author       string `json:"author"`
	AuthorStatus string `json:"author_status"`
	Body         string `json:"body"`
	Lane         string `json:"lane"`
	BlockedOn    string `json:"blocked_on"`
}

// PacketDiffFile is one reviewable file's patch. For small modified files the
// full head-side content is attached too: the patch stays the ground truth for
// what changed, while full_content gives the surrounding code (and real head
// line numbers) a bare hunk can't.
type PacketDiffFile struct {
	Path             string `json:"path"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Patch            string `json:"patch"`
	PatchTruncated   bool   `json:"patch_truncated"`
	FullContent      string `json:"full_content,omitempty"`
	FullContentLines int    `json:"full_content_lines,omitempty"`
	// Commentable is the head-side (RIGHT) line ranges of the shown hunks, e.g.
	// "12-34, 50-61" — the lines a RIGHT-side comment may anchor to. Surfaced so
	// the resident anchors inside a hunk instead of guessing a line past the diff.
	Commentable string `json:"commentable,omitempty"`
}

// OmittedFile is a changed file whose patch is not in the packet, with the
// reason the resident needs to reason about it: "no-patch" (binary, or a diff
// neither the files API nor the raw PR diff carried), or a deliberate skip ("lockfile"/"generated"/
// "vendored") whose diff costs tokens but carries little review signal.
type OmittedFile struct {
	Path      string `json:"path"`
	Reason    string `json:"reason"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// PacketDiff is the PR's net diff plus what could not be (or was not) read.
type PacketDiff struct {
	Files      []PacketDiffFile `json:"files"`
	Omitted    []OmittedFile    `json:"omitted"`
	TotalFiles int              `json:"total_files"`
	ShownFiles int              `json:"shown_files"`
}

// PacketIssueComment is one comment on a linked issue.
type PacketIssueComment struct {
	Author string `json:"author"`
	Body   string `json:"body"`
}

// PacketIssue is one issue the PR closes, with its discussion — the premise
// context: the problem the change claims to solve, plus the refining discussion.
type PacketIssue struct {
	Number   int                  `json:"number"`
	Title    string               `json:"title"`
	State    string               `json:"state"`
	Body     string               `json:"body"`
	Comments []PacketIssueComment `json:"comments,omitempty"`
}

// Packet is the full deterministic input to a fresh review.
type Packet struct {
	PR           PacketPR       `json:"pr"`
	Acuity       prr.Acuity     `json:"acuity"`
	Effort       prr.Effort     `json:"effort"`
	Escalation   prr.Escalation `json:"escalation"`
	MergeState   prr.MergeState `json:"merge_state"`
	Diff         PacketDiff     `json:"diff"`
	LinkedIssues []PacketIssue  `json:"linked_issues,omitempty"`
	LaneNote     string         `json:"lane_note,omitempty"`
}

// Fetcher is the GitHub surface packet building needs. *gh.Client satisfies it.
type Fetcher interface {
	ViewerLogin() (string, error)
	PullFiles(owner, name string, number int) ([]gh.FileDiff, error)
	Compare(owner, name, base, head string) (gh.CompareResult, error)
	FetchReReviewData(owner, name string, number int) (gh.ReReviewPR, error)
	FileContent(owner, name, path, ref string) (string, error)
	FetchLinkedIssues(owner, name string, number int) ([]gh.LinkedIssue, error)
}

// contentFetcher is the slice of Fetcher used to enrich small files with their
// full head-side content. A nil contentFetcher disables enrichment.
type contentFetcher interface {
	FileContent(owner, name, path, ref string) (string, error)
}

// smallFileMaxLines caps which modified files get full head-side content
// attached — small enough that the whole file is cheap context.
const smallFileMaxLines = 400

// budget bounds a packet's diff size in estimated input tokens. Patches are
// ground truth and fill up to hardTokens; full_content is context and is only
// attached while under softTokens. A zero field means "no limit".
type budget struct {
	softTokens int
	hardTokens int
}

// defaultBudget targets ~5% of a 5-hour window per review: full_content stops
// being attached past ~80k tokens of diff, and no packet's diff exceeds ~150k.
func defaultBudget() budget { return budget{softTokens: 80_000, hardTokens: 150_000} }

// estTokens is a cheap chars/4 approximation of an English/code token count.
func estTokens(s string) int { return len(s) / 4 }

// SkipRules classifies changed files whose diffs are low review signal so they
// are surfaced in the packet's omitted list rather than shown in full. The zero
// value skips nothing; DefaultSkipRules carries the built-ins and config
// extends it (see skipRulesFromConfig). Vendored/generated paths are matched as
// directory segments.
type SkipRules struct {
	Lockfiles         []string // exact base names, e.g. "Cargo.lock"
	VendoredSegments  []string // directory segments, e.g. "vendor/"
	GeneratedSegments []string // directory segments, e.g. "generated/"
	GeneratedSuffixes []string // filename suffixes, e.g. ".pb.go"
}

// DefaultSkipRules is the built-in classifier, tuned against the configured
// repos. Snapshots (.snap) and sqllogictest data (.slt) are deliberately NOT
// skipped — a snapshot/expected-output diff is often the review subject.
func DefaultSkipRules() SkipRules {
	return SkipRules{
		Lockfiles: []string{
			"package-lock.json", "yarn.lock", "pnpm-lock.yaml",
			"Cargo.lock", "go.sum", "poetry.lock",
			"Gemfile.lock", "composer.lock", "flake.lock",
			"uv.lock", "pdm.lock", "bun.lockb",
		},
		VendoredSegments: []string{"vendor/", "node_modules/", "third_party/"},
		// datafusion commits its protobuf under src/generated/*.rs, named
		// nothing like a .pb.rs; the sibling generator dir gen/ is real source.
		GeneratedSegments: []string{"generated/"},
		GeneratedSuffixes: []string{
			".pb.go", ".pb.rs", "_pb2.py", "_pb2_grpc.py",
			".generated.go", "_generated.go", ".gen.go",
			".min.js", ".min.css",
		},
	}
}

// extend returns the rules with extra entries appended — config extends the
// built-in defaults rather than replacing them.
func (r SkipRules) extend(extra SkipRules) SkipRules {
	cat := func(a, b []string) []string { return append(append([]string{}, a...), b...) }
	return SkipRules{
		Lockfiles:         cat(r.Lockfiles, extra.Lockfiles),
		VendoredSegments:  cat(r.VendoredSegments, dirSegments(extra.VendoredSegments)),
		GeneratedSegments: cat(r.GeneratedSegments, dirSegments(extra.GeneratedSegments)),
		GeneratedSuffixes: cat(r.GeneratedSuffixes, extra.GeneratedSuffixes),
	}
}

// classify marks a path as machine-generated / vendored / lock, or shown.
func (r SkipRules) classify(path string) (string, bool) {
	base := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		base = path[i+1:]
	}
	for _, lf := range r.Lockfiles {
		if base == lf {
			return "lockfile", true
		}
	}
	if hasSegment(path, r.VendoredSegments) {
		return "vendored", true
	}
	if hasSegment(path, r.GeneratedSegments) {
		return "generated", true
	}
	for _, suf := range r.GeneratedSuffixes {
		if strings.HasSuffix(base, suf) {
			return "generated", true
		}
	}
	return "", false
}

// dirSegments normalizes user-supplied directory names to the trailing-slash
// form hasSegment matches on ("vendor" -> "vendor/").
func dirSegments(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if !strings.HasSuffix(s, "/") {
			s += "/"
		}
		out = append(out, s)
	}
	return out
}

func hasSegment(path string, segs []string) bool {
	for _, seg := range segs {
		if strings.HasPrefix(path, seg) || strings.Contains(path, "/"+seg) {
			return true
		}
	}
	return false
}

// skipRulesFromConfig builds the effective classifier: the built-in defaults
// extended with the config's diff.skip lists.
func skipRulesFromConfig(cfg *config.Config) SkipRules {
	rules := DefaultSkipRules()
	if cfg == nil {
		return rules
	}
	s := cfg.Diff.Skip
	return rules.extend(SkipRules{
		Lockfiles:         s.Lockfiles,
		VendoredSegments:  s.VendoredPaths,
		GeneratedSegments: s.GeneratedPaths,
		GeneratedSuffixes: s.GeneratedSuffixes,
	})
}

// splitHunks groups a unified-diff patch into its leading header (if any) and
// one block per hunk (each starting with an "@@" line).
func splitHunks(lines []string) [][]string {
	var hunks [][]string
	var cur []string
	for _, ln := range lines {
		if strings.HasPrefix(ln, "@@") && len(cur) > 0 {
			hunks = append(hunks, cur)
			cur = nil
		}
		cur = append(cur, ln)
	}
	if len(cur) > 0 {
		hunks = append(hunks, cur)
	}
	return hunks
}

// truncatePatchToHunks keeps whole diff hunks up to maxLines, eliding the rest
// with a marker. Dropping whole hunks (rather than a flat first-N-lines cut)
// keeps every shown hunk's line numbers aligned. A single hunk larger than
// maxLines is line-truncated as a last resort.
func truncatePatchToHunks(patch string, maxLines int) (string, bool) {
	if patch == "" || maxLines <= 0 {
		return patch, false
	}
	lines := strings.Split(patch, "\n")
	if len(lines) <= maxLines {
		return patch, false
	}
	var kept []string
	for _, h := range splitHunks(lines) {
		if len(kept) > 0 && len(kept)+len(h) > maxLines {
			break // this hunk (and the rest) don't fit; elide from here
		}
		if len(kept)+len(h) > maxLines {
			// First block alone overflows: line-truncate it.
			kept = append(kept, h[:maxLines-len(kept)]...)
			break
		}
		kept = append(kept, h...)
	}
	elided := len(lines) - len(kept)
	marker := fmt.Sprintf("... [%d line(s) elided to fit review budget] ...", elided)
	return strings.Join(append(kept, marker), "\n"), true
}

// wantsFullContent reports whether a file is a candidate for full head-side
// content: an edit (not a pure add/delete) whose change is small enough that the
// file plausibly fits under smallFileMaxLines. It bounds the fetch; the fetched
// content's real line count is the definitive gate.
func wantsFullContent(f gh.FileDiff) bool {
	switch f.Status {
	case "modified", "renamed", "changed":
		return f.Additions+f.Deletions <= 2*smallFileMaxLines
	}
	return false
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// assembleDiff turns raw changed files into the packet's shown patches plus the
// reasoned omitted list: binary/no-patch files, deliberately-skipped junk
// (generated/vendored/lock), and oversized patches hunk-elided to a soft cap.
// When cf is non-nil, small edited files get their full head-side content (at
// ref) attached for context. The budget bounds total size: under pressure it
// sheds full_content first (context), then whole patches (reason "budget") —
// patches are ground truth, so they win the last tokens.
func assembleDiff(files []gh.FileDiff, cf contentFetcher, owner, name, ref string, b budget, rules SkipRules) (shown []PacketDiffFile, omitted []OmittedFile) {
	used := 0
	for _, f := range files {
		if f.Patch == "" { // binary, or a diff not even the raw PR diff carried
			omitted = append(omitted, OmittedFile{Path: f.Filename, Reason: "no-patch", Additions: f.Additions, Deletions: f.Deletions})
			continue
		}
		if reason, skip := rules.classify(f.Filename); skip {
			omitted = append(omitted, OmittedFile{Path: f.Filename, Reason: reason, Additions: f.Additions, Deletions: f.Deletions})
			continue
		}
		patch, truncated := truncatePatchToHunks(f.Patch, maxPatchLines)
		ptok := estTokens(patch)
		if b.hardTokens > 0 && used+ptok > b.hardTokens {
			omitted = append(omitted, OmittedFile{Path: f.Filename, Reason: "budget", Additions: f.Additions, Deletions: f.Deletions})
			continue
		}
		used += ptok
		pf := PacketDiffFile{
			Path: f.Filename, Status: f.Status,
			Additions: f.Additions, Deletions: f.Deletions,
			Patch: patch, PatchTruncated: truncated,
			Commentable: commentableRanges(patch),
		}
		if cf != nil && wantsFullContent(f) && (b.softTokens == 0 || used < b.softTokens) {
			if content, err := cf.FileContent(owner, name, f.Filename, ref); err == nil && content != "" {
				if n := countLines(content); n <= smallFileMaxLines {
					if ctok := estTokens(content); b.hardTokens == 0 || used+ctok <= b.hardTokens {
						pf.FullContent = content
						pf.FullContentLines = n
						used += ctok
					}
				}
			}
		}
		shown = append(shown, pf)
	}
	return shown, omitted
}

// commentableRanges renders a patch's head-side hunk lines — the lines a
// RIGHT-side comment may anchor to — as a compact "a-b, c-d" string. Empty when
// the patch has no shown hunks.
func commentableRanges(patch string) string {
	right, _ := gh.CommentableLines(patch)
	if len(right) == 0 {
		return ""
	}
	lines := make([]int, 0, len(right))
	for ln := range right {
		lines = append(lines, ln)
	}
	sort.Ints(lines)
	var parts []string
	start, prev := lines[0], lines[0]
	flush := func() {
		if start == prev {
			parts = append(parts, strconv.Itoa(start))
		} else {
			parts = append(parts, strconv.Itoa(start)+"-"+strconv.Itoa(prev))
		}
	}
	for _, ln := range lines[1:] {
		if ln == prev+1 {
			prev = ln
			continue
		}
		flush()
		start, prev = ln, ln
	}
	flush()
	return strings.Join(parts, ", ")
}

// BuildPacket assembles a review packet from an already-derived record plus the
// PR's net diff fetched via ff. The record carries the deterministic baseline
// (acuity/effort/escalation/merge_state) so no re-derivation happens here.
// rules is the effective file classifier (defaults extended by config).
func BuildPacket(ff Fetcher, r *prr.Record, rules SkipRules) (Packet, error) {
	owner, name := splitRepo(r.Repo)
	files, err := ff.PullFiles(owner, name, r.Number)
	if err != nil {
		return Packet{}, err
	}

	diffFiles, omitted := assembleDiff(files, ff, owner, name, r.HeadOid, defaultBudget(), rules)

	// Linked issues are premise context, not load-bearing: a fetch failure
	// degrades the review (no problem statement) but must not fail the packet.
	var issues []PacketIssue
	if li, err := ff.FetchLinkedIssues(owner, name, r.Number); err == nil {
		issues = assembleIssues(li)
	}

	var laneNote string
	if r.Lane != "fresh" {
		laneNote = "this PR is in the " + r.Lane + " lane (blocked_on=" + r.BlockedOn +
			") — you have a prior review; a delta re-review would be more focused. " +
			"Packet built for a full re-read."
	}

	return Packet{
		PR: PacketPR{
			Repo: r.Repo, Number: r.Number, Title: r.Title, URL: r.URL,
			Head: r.HeadOid, Author: r.Author, AuthorStatus: r.AuthorStatus,
			Body: r.Body, Lane: r.Lane, BlockedOn: r.BlockedOn,
		},
		Acuity:     r.Acuity,
		Effort:     r.Effort,
		Escalation: r.Escalation,
		MergeState: r.MergeState,
		Diff: PacketDiff{
			Files: diffFiles, Omitted: omitted,
			TotalFiles: len(files), ShownFiles: len(diffFiles),
		},
		LinkedIssues: issues,
		LaneNote:     laneNote,
	}, nil
}

// Bounds on linked-issue context. The problem statement and its refining
// discussion are worth reading, but an issue with hundreds of comments must not
// dominate the packet — bodies and comments are truncated and the comment list
// is capped, with a marker so the resident knows it didn't see everything.
const (
	issueBodyMaxChars    = 6000
	issueCommentMaxChars = 2000
	maxIssueComments     = 30
)

// truncateChars clips s to max runes, appending an ellipsis marker if it clipped.
func truncateChars(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "\n…[truncated]"
}

// assembleIssues converts fetched linked issues into bounded packet form.
func assembleIssues(issues []gh.LinkedIssue) []PacketIssue {
	var out []PacketIssue
	for _, is := range issues {
		pi := PacketIssue{
			Number: is.Number, Title: is.Title, State: is.State,
			Body: truncateChars(is.Body, issueBodyMaxChars),
		}
		for i, c := range is.Comments {
			if i >= maxIssueComments {
				pi.Comments = append(pi.Comments, PacketIssueComment{
					Body: fmt.Sprintf("…[%d more comments omitted]", len(is.Comments)-maxIssueComments),
				})
				break
			}
			pi.Comments = append(pi.Comments, PacketIssueComment{
				Author: c.Author, Body: truncateChars(c.Body, issueCommentMaxChars),
			})
		}
		out = append(out, pi)
	}
	return out
}

func splitRepo(repo string) (owner, name string) {
	if i := strings.IndexByte(repo, '/'); i >= 0 {
		return repo[:i], repo[i+1:]
	}
	return repo, ""
}
