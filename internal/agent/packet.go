// Package agent builds review packets, runs the review agent (headless Claude
// Code first), and orchestrates a dispatch round. The deterministic packet is
// built here in Go; the agent only exercises judgment over it and returns a SOAP.
package agent

import (
	"fmt"
	"strings"

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
}

// OmittedFile is a changed file whose patch is not in the packet, with the
// reason the resident needs to reason about it: "no-patch" (binary/too-large;
// GitHub gave no patch), or a deliberate skip ("lockfile"/"generated"/
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

// Packet is the full deterministic input to a fresh review.
type Packet struct {
	PR         PacketPR       `json:"pr"`
	Acuity     prr.Acuity     `json:"acuity"`
	Effort     prr.Effort     `json:"effort"`
	Escalation prr.Escalation `json:"escalation"`
	MergeState prr.MergeState `json:"merge_state"`
	Diff       PacketDiff     `json:"diff"`
	LaneNote   string         `json:"lane_note,omitempty"`
}

// Fetcher is the GitHub surface packet building needs. *gh.Client satisfies it.
type Fetcher interface {
	ViewerLogin() (string, error)
	PullFiles(owner, name string, number int) ([]gh.FileDiff, error)
	Compare(owner, name, base, head string) (gh.CompareResult, error)
	FetchReReviewData(owner, name string, number int) (gh.ReReviewPR, error)
	FileContent(owner, name, path, ref string) (string, error)
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

var lockFiles = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"Cargo.lock": true, "go.sum": true, "poetry.lock": true,
	"Gemfile.lock": true, "composer.lock": true, "flake.lock": true,
	"uv.lock": true, "pdm.lock": true, "bun.lockb": true,
}

var vendorSegments = []string{"vendor/", "node_modules/", "third_party/"}

// generatedSegments are directory names whose subtree is committed machine-
// generated code (e.g. datafusion's protobuf under src/generated/*.rs, which is
// named nothing like a .pb.rs). The sibling generator dir (gen/) is real source
// and deliberately not matched.
var generatedSegments = []string{"generated/"}

var generatedSuffixes = []string{
	".pb.go", ".pb.rs", "_pb2.py", "_pb2_grpc.py",
	".generated.go", "_generated.go", ".gen.go",
	".min.js", ".min.css",
}

func hasSegment(path string, segs []string) bool {
	for _, seg := range segs {
		if strings.HasPrefix(path, seg) || strings.Contains(path, "/"+seg) {
			return true
		}
	}
	return false
}

// classifyFile marks machine-generated / vendored / lock files whose diffs cost
// tokens but carry little review signal. Returns a reason and true to skip.
// Note: test snapshots (.snap) and sqllogictest data (.slt) are deliberately
// NOT skipped — a snapshot/expected-output diff is often the review subject.
func classifyFile(path string) (string, bool) {
	base := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		base = path[i+1:]
	}
	switch {
	case lockFiles[base]:
		return "lockfile", true
	case hasSegment(path, vendorSegments):
		return "vendored", true
	case hasSegment(path, generatedSegments):
		return "generated", true
	}
	for _, suf := range generatedSuffixes {
		if strings.HasSuffix(base, suf) {
			return "generated", true
		}
	}
	return "", false
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
func assembleDiff(files []gh.FileDiff, cf contentFetcher, owner, name, ref string, b budget) (shown []PacketDiffFile, omitted []OmittedFile) {
	used := 0
	for _, f := range files {
		if f.Patch == "" { // GitHub omits patch for binary / very large files
			omitted = append(omitted, OmittedFile{Path: f.Filename, Reason: "no-patch", Additions: f.Additions, Deletions: f.Deletions})
			continue
		}
		if reason, skip := classifyFile(f.Filename); skip {
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

// BuildPacket assembles a review packet from an already-derived record plus the
// PR's net diff fetched via ff. The record carries the deterministic baseline
// (acuity/effort/escalation/merge_state) so no re-derivation happens here.
func BuildPacket(ff Fetcher, r *prr.Record) (Packet, error) {
	owner, name := splitRepo(r.Repo)
	files, err := ff.PullFiles(owner, name, r.Number)
	if err != nil {
		return Packet{}, err
	}

	diffFiles, omitted := assembleDiff(files, ff, owner, name, r.HeadOid, defaultBudget())

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
		LaneNote: laneNote,
	}, nil
}

func splitRepo(repo string) (owner, name string) {
	if i := strings.IndexByte(repo, '/'); i >= 0 {
		return repo[:i], repo[i+1:]
	}
	return repo, ""
}
