package agent

import (
	"testing"

	"github.com/wjones127/pr-residents/internal/gh"
	"github.com/wjones127/pr-residents/internal/prr"
)

func TestBuildDeltaNoAnchor(t *testing.T) {
	d := buildDelta("", "head", nil, nil)
	if d.Note == "" || len(d.Files) != 0 {
		t.Errorf("no-anchor delta: %+v", d)
	}
}

func TestBuildDeltaHeadUnchanged(t *testing.T) {
	d := buildDelta("same", "same", nil, nil)
	if d.Note != "head unchanged since last review; delta is empty" {
		t.Errorf("head-unchanged note: %q", d.Note)
	}
}

func TestBuildDeltaAheadScopesToNet(t *testing.T) {
	cmp := &gh.CompareResult{
		Status: "ahead",
		Commits: []struct {
			SHA string `json:"sha"`
		}{{SHA: "c1"}},
		Files: []gh.FileDiff{
			{Filename: "a.go", Patch: "@@ a"},        // in net
			{Filename: "vendor/x.go", Patch: "@@ v"}, // churn from base merge, not in net
		},
	}
	net := []gh.FileDiff{{Filename: "a.go", Patch: "@@ a"}}
	d := buildDelta("old", "head", cmp, net)
	if d.CompareStatus != "ahead" || d.AnchorOrphaned {
		t.Errorf("status: %+v", d)
	}
	if len(d.Files) != 1 || d.Files[0].Path != "a.go" || d.FilesOffBranchExcluded != 1 {
		t.Errorf("scoping: files=%+v excluded=%d", d.Files, d.FilesOffBranchExcluded)
	}
}

func TestBuildDeltaOrphanedAnchorFallsBackToNet(t *testing.T) {
	cmp := &gh.CompareResult{Status: "diverged"} // rebase/force-push
	net := []gh.FileDiff{{Filename: "a.go", Patch: "@@ real patch"}}
	d := buildDelta("old", "head", cmp, net)
	if !d.AnchorOrphaned || len(d.Files) != 1 || d.Files[0].Path != "a.go" {
		t.Errorf("orphaned anchor should fall back to net diff: %+v", d)
	}
}

func TestBuildDeltaBackfillsPatchFromNet(t *testing.T) {
	// Compare omits the patch (huge range); net carries the real one.
	cmp := &gh.CompareResult{Status: "ahead", Files: []gh.FileDiff{{Filename: "a.go", Patch: ""}}}
	net := []gh.FileDiff{{Filename: "a.go", Patch: "@@ real", Additions: 3}}
	d := buildDelta("old", "head", cmp, net)
	if len(d.Files) != 1 || d.Files[0].Patch != "@@ real" || d.Files[0].Additions != 3 {
		t.Errorf("patch backfill: %+v", d.Files)
	}
}

func TestLastReviewedSHA(t *testing.T) {
	reviews := []gh.Review{
		{Author: &gh.Actor{Login: "me"}, SubmittedAt: "2026-06-20T00:00:00Z", Commit: &gh.Commit{Oid: "old"}},
		{Author: &gh.Actor{Login: "me"}, SubmittedAt: "2026-06-22T00:00:00Z", Commit: &gh.Commit{Oid: "newer"}},
		{Author: &gh.Actor{Login: "other"}, SubmittedAt: "2026-06-23T00:00:00Z", Commit: &gh.Commit{Oid: "theirs"}},
	}
	if got := lastReviewedSHA(reviews, "me"); got != "newer" {
		t.Errorf("lastReviewedSHA = %q want newer", got)
	}
	if got := lastReviewedSHA(nil, "me"); got != "" {
		t.Errorf("no reviews should be empty, got %q", got)
	}
}

// rrFetcher is a fetcher stub for the re-review packet path.
type rrFetcher struct {
	pr  gh.ReReviewPR
	cmp gh.CompareResult
	net []gh.FileDiff
}

func (f rrFetcher) ViewerLogin() (string, error) { return "me", nil }
func (f rrFetcher) PullFiles(owner, name string, number int) ([]gh.FileDiff, error) {
	return f.net, nil
}
func (f rrFetcher) Compare(owner, name, base, head string) (gh.CompareResult, error) {
	return f.cmp, nil
}
func (f rrFetcher) FetchReReviewData(owner, name string, number int) (gh.ReReviewPR, error) {
	return f.pr, nil
}

func TestBuildReReviewPacket(t *testing.T) {
	f := rrFetcher{
		pr: gh.ReReviewPR{
			Number: 5, Title: "t", URL: "u", Head: "head", Mergeable: "MERGEABLE", RollupState: "SUCCESS",
			Reviews: []gh.Review{{Author: &gh.Actor{Login: "me"}, SubmittedAt: "2026-06-22T00:00:00Z", Commit: &gh.Commit{Oid: "old"}}},
			Threads: []gh.ReviewThread{{ID: "t1", RootAuthor: "me", RootBody: "issue(blocking): guard nil", Path: "a.go", Line: 3}},
		},
		cmp: gh.CompareResult{Status: "ahead", Files: []gh.FileDiff{{Filename: "a.go", Patch: "@@ fix"}}},
		net: []gh.FileDiff{{Filename: "a.go", Patch: "@@ fix"}},
	}
	rec := &prr.Record{Repo: "o/r", Number: 5, Lane: "re_review"}
	pkt, err := BuildReReviewPacket(f, rec, "me")
	if err != nil {
		t.Fatal(err)
	}
	if pkt.PR.LastReviewedSHA != "old" || pkt.PR.Head != "head" {
		t.Errorf("pr span: %+v", pkt.PR)
	}
	if len(pkt.Conditions) != 1 || pkt.Conditions[0].Kind != "blocking" {
		t.Errorf("conditions: %+v", pkt.Conditions)
	}
	if len(pkt.Delta.Files) != 1 || pkt.Delta.CompareStatus != "ahead" {
		t.Errorf("delta: %+v", pkt.Delta)
	}
	if pkt.MergeState.CI != "green" {
		t.Errorf("merge state CI: %+v", pkt.MergeState)
	}
}
