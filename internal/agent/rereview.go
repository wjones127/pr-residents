package agent

import (
	"fmt"

	"github.com/wjones127/pr-residents/internal/derive"
	"github.com/wjones127/pr-residents/internal/gh"
	"github.com/wjones127/pr-residents/internal/prr"
)

// ReReviewPR is the re-review packet's PR identity, spanning last-reviewed → head.
type ReReviewPR struct {
	Repo            string `json:"repo"`
	Number          int    `json:"number"`
	Title           string `json:"title"`
	URL             string `json:"url"`
	Head            string `json:"head"`
	LastReviewedSHA string `json:"last_reviewed_sha"`
	Viewer          string `json:"viewer"`
}

// Delta is the commit-anchored diff since last review, scoped to the PR's own
// files. When the anchor is orphaned (rebase/force-push), it degrades to the
// full net diff with AnchorOrphaned set.
type Delta struct {
	Base                   string           `json:"base"`
	Head                   string           `json:"head"`
	Files                  []PacketDiffFile `json:"files"`
	CommitCount            int              `json:"commit_count"`
	FilesOffBranchExcluded int              `json:"files_off_branch_excluded"`
	AnchorOrphaned         bool             `json:"anchor_orphaned"`
	CompareStatus          string           `json:"compare_status"`
	Note                   string           `json:"note"`
}

// ReReviewPacket is the full deterministic input to a re-review.
type ReReviewPacket struct {
	PR         ReReviewPR     `json:"pr"`
	MergeState prr.MergeState `json:"merge_state"`
	Conditions []Condition    `json:"conditions"`
	Delta      Delta          `json:"delta"`
}

func lastReviewedSHA(reviews []gh.Review, viewer string) string {
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
	if latest == nil || latest.Commit == nil {
		return ""
	}
	return latest.Commit.Oid
}

func scopeDeltaFiles(compareFiles []gh.FileDiff, netByPath map[string]gh.FileDiff) ([]gh.FileDiff, int) {
	var kept []gh.FileDiff
	for _, f := range compareFiles {
		if _, ok := netByPath[f.Filename]; ok {
			kept = append(kept, f)
		}
	}
	return kept, len(compareFiles) - len(kept)
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// buildDelta assembles the re-review delta from already-fetched inputs (pure,
// testable). cmp is compare(lastReviewed...head) (nil when no anchor / head
// unchanged); prNetFull is the PR's net changed files with patches.
func buildDelta(lastReviewed, head string, cmp *gh.CompareResult, prNetFull []gh.FileDiff) Delta {
	d := Delta{Base: lastReviewed, Head: head, Files: []PacketDiffFile{}}
	if lastReviewed == "" {
		d.Note = "no prior review by viewer; nothing to anchor a delta on"
		return d
	}
	if lastReviewed == head {
		d.Note = "head unchanged since last review; delta is empty"
		return d
	}

	netByPath := map[string]gh.FileDiff{}
	for _, f := range prNetFull {
		netByPath[f.Filename] = f
	}
	if cmp != nil {
		d.CompareStatus = cmp.Status
		d.CommitCount = len(cmp.Commits)
	}

	var kept []gh.FileDiff
	if d.CompareStatus == "ahead" {
		// Clean linear progress: scope the compare files to the author's own net
		// changes so a branch that merged its base in isn't swamped by that churn.
		var cmpFiles []gh.FileDiff
		if cmp != nil {
			cmpFiles = cmp.Files
		}
		var excluded int
		kept, excluded = scopeDeltaFiles(cmpFiles, netByPath)
		d.FilesOffBranchExcluded = excluded
		if excluded > 0 {
			d.Note = fmt.Sprintf("scoped to author's changes: excluded %d files that changed on the branch via merges from the base branch, not the PR's own work", excluded)
		}
	} else {
		// Orphaned anchor (diverged/behind/rebased): no valid delta — fall back
		// to the full PR net diff and flag it.
		d.AnchorOrphaned = true
		kept = append([]gh.FileDiff{}, prNetFull...)
		d.Note = fmt.Sprintf("prior-review anchor %s is orphaned (compare status=%s: branch rebased/force-pushed) — showing full PR net diff, verify conditions fresh-eyes", short(lastReviewed), d.CompareStatus)
	}

	for _, f := range kept {
		patch := f.Patch
		if patch == "" { // backfill patch/counts from the net diff (orphaned-anchor case)
			if alt, ok := netByPath[f.Filename]; ok && alt.Patch != "" {
				f = alt
				patch = alt.Patch
			}
		}
		p, truncated := truncatePatch(patch)
		d.Files = append(d.Files, PacketDiffFile{
			Path: f.Filename, Status: f.Status,
			Additions: f.Additions, Deletions: f.Deletions,
			Patch: p, PatchTruncated: truncated,
		})
	}
	return d
}

// BuildReReviewPacket assembles a re-review packet: the reconstructed conditions
// ledger plus the commit-anchored delta. viewer identifies whose reviews/threads
// anchor the ledger and delta.
func BuildReReviewPacket(f Fetcher, r *prr.Record, viewer string) (ReReviewPacket, error) {
	owner, name := splitRepo(r.Repo)
	pr, err := f.FetchReReviewData(owner, name, r.Number)
	if err != nil {
		return ReReviewPacket{}, err
	}
	head := pr.Head
	last := lastReviewedSHA(pr.Reviews, viewer)
	ledger := ReconstructConditions(pr.Threads, viewer)

	var cmp *gh.CompareResult
	var net []gh.FileDiff
	if last != "" && last != head {
		c, err := f.Compare(owner, name, last, head)
		if err != nil {
			return ReReviewPacket{}, err
		}
		cmp = &c
		net, err = f.PullFiles(owner, name, r.Number)
		if err != nil {
			return ReReviewPacket{}, err
		}
	}
	delta := buildDelta(last, head, cmp, net)

	// CI/mergeability computed with the same mapping as the synced record.
	msDetail := &gh.Detail{
		Mergeable: pr.Mergeable,
		Commits: gh.Commits{Nodes: []gh.CommitNode{{Commit: gh.Commit{StatusCheckRollup: &gh.Rollup{
			State:    pr.RollupState,
			Contexts: gh.RollupContexts{Nodes: pr.RollupContexts},
		}}}}},
	}

	return ReReviewPacket{
		PR: ReReviewPR{
			Repo: r.Repo, Number: r.Number, Title: pr.Title, URL: pr.URL,
			Head: head, LastReviewedSHA: last, Viewer: viewer,
		},
		MergeState: derive.MergeStateFromDetail(msDetail),
		Conditions: ledger,
		Delta:      delta,
	}, nil
}
