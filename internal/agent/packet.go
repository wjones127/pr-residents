// Package agent builds review packets, runs the review agent (headless Claude
// Code first), and orchestrates a dispatch round. The deterministic packet is
// built here in Go; the agent only exercises judgment over it and returns a SOAP.
package agent

import (
	"strings"

	"github.com/wjones127/pr-residents/internal/gh"
	"github.com/wjones127/pr-residents/internal/prr"
)

const maxPatchLines = 500

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

// PacketDiff is the PR's net diff plus what could not be read.
type PacketDiff struct {
	Files      []PacketDiffFile `json:"files"`
	Omitted    []string         `json:"omitted"`
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

func truncatePatch(patch string) (string, bool) {
	if patch == "" {
		return patch, false
	}
	lines := strings.Split(patch, "\n")
	if len(lines) <= maxPatchLines {
		return patch, false
	}
	return strings.Join(lines[:maxPatchLines], "\n"), true
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

	var diffFiles []PacketDiffFile
	var omitted []string
	for _, f := range files {
		if f.Patch == "" { // GitHub omits patch for binary / very large files
			omitted = append(omitted, f.Filename)
			continue
		}
		patch, truncated := truncatePatch(f.Patch)
		diffFiles = append(diffFiles, PacketDiffFile{
			Path: f.Filename, Status: f.Status,
			Additions: f.Additions, Deletions: f.Deletions,
			Patch: patch, PatchTruncated: truncated,
		})
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
		LaneNote: laneNote,
	}, nil
}

func splitRepo(repo string) (owner, name string) {
	if i := strings.IndexByte(repo, '/'); i >= 0 {
		return repo[:i], repo[i+1:]
	}
	return repo, ""
}
