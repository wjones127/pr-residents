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

// PacketDiffFile is one reviewable file's patch.
type PacketDiffFile struct {
	Path           string `json:"path"`
	Status         string `json:"status"`
	Additions      int    `json:"additions"`
	Deletions      int    `json:"deletions"`
	Patch          string `json:"patch"`
	PatchTruncated bool   `json:"patch_truncated"`
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
}

var lockFiles = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"Cargo.lock": true, "go.sum": true, "poetry.lock": true,
	"Gemfile.lock": true, "composer.lock": true, "flake.lock": true,
}

var vendorSegments = []string{"vendor/", "node_modules/", "third_party/"}

var generatedSuffixes = []string{
	".pb.go", ".pb.rs", "_pb2.py", "_pb2_grpc.py",
	".generated.go", "_generated.go", ".gen.go",
	".min.js", ".min.css", ".snap",
}

// classifyFile marks machine-generated / vendored / lock files whose diffs cost
// tokens but carry little review signal. Returns a reason and true to skip.
func classifyFile(path string) (string, bool) {
	base := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		base = path[i+1:]
	}
	if lockFiles[base] {
		return "lockfile", true
	}
	for _, seg := range vendorSegments {
		if strings.HasPrefix(path, seg) || strings.Contains(path, "/"+seg) {
			return "vendored", true
		}
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

// assembleDiff turns raw changed files into the packet's shown patches plus the
// reasoned omitted list: binary/no-patch files, deliberately-skipped junk
// (generated/vendored/lock), and oversized patches hunk-elided to a soft cap.
func assembleDiff(files []gh.FileDiff) (shown []PacketDiffFile, omitted []OmittedFile) {
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
		shown = append(shown, PacketDiffFile{
			Path: f.Filename, Status: f.Status,
			Additions: f.Additions, Deletions: f.Deletions,
			Patch: patch, PatchTruncated: truncated,
		})
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

	diffFiles, omitted := assembleDiff(files)

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
