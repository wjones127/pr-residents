package agent

import (
	"strings"
	"testing"

	"github.com/lancedb/pr-residents/internal/gh"
	"github.com/lancedb/pr-residents/internal/prr"
)

type fakeFetcher struct {
	files []gh.FileDiff
	err   error
}

func (f fakeFetcher) PullFiles(owner, name string, number int) ([]gh.FileDiff, error) {
	return f.files, f.err
}

func (f fakeFetcher) ViewerLogin() (string, error) { return "me", nil }

func (f fakeFetcher) Compare(owner, name, base, head string) (gh.CompareResult, error) {
	return gh.CompareResult{}, nil
}

func (f fakeFetcher) FetchReReviewData(owner, name string, number int) (gh.ReReviewPR, error) {
	return gh.ReReviewPR{}, nil
}

func TestBuildPacketPartitionsAndTruncates(t *testing.T) {
	big := strings.Repeat("x\n", maxPatchLines+50)
	ff := fakeFetcher{files: []gh.FileDiff{
		{Filename: "a.go", Status: "modified", Additions: 3, Deletions: 1, Patch: "@@ -1 +1 @@\n-x\n+y"},
		{Filename: "img.png", Status: "added"}, // no patch -> omitted
		{Filename: "big.txt", Status: "modified", Patch: big},
	}}
	r := &prr.Record{Repo: "o/r", Number: 5, Title: "t", URL: "u", HeadOid: "abc", Lane: "fresh"}

	p, err := BuildPacket(ff, r)
	if err != nil {
		t.Fatal(err)
	}
	if p.Diff.TotalFiles != 3 || p.Diff.ShownFiles != 2 {
		t.Errorf("counts: total=%d shown=%d", p.Diff.TotalFiles, p.Diff.ShownFiles)
	}
	if len(p.Diff.Omitted) != 1 || p.Diff.Omitted[0] != "img.png" {
		t.Errorf("omitted: %+v", p.Diff.Omitted)
	}
	var bigFile *PacketDiffFile
	for i := range p.Diff.Files {
		if p.Diff.Files[i].Path == "big.txt" {
			bigFile = &p.Diff.Files[i]
		}
	}
	if bigFile == nil || !bigFile.PatchTruncated {
		t.Errorf("big.txt should be truncated: %+v", bigFile)
	}
	if strings.Count(bigFile.Patch, "\n") >= maxPatchLines+1 {
		t.Errorf("patch not truncated to %d lines", maxPatchLines)
	}
	if p.LaneNote != "" {
		t.Errorf("fresh lane should have no lane note, got %q", p.LaneNote)
	}
}

func TestBuildPacketLaneNoteForReReview(t *testing.T) {
	ff := fakeFetcher{files: []gh.FileDiff{{Filename: "a.go", Patch: "@@"}}}
	r := &prr.Record{Repo: "o/r", Number: 5, Lane: "re_review", BlockedOn: "me"}
	p, err := BuildPacket(ff, r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.LaneNote, "re_review") {
		t.Errorf("expected a re_review lane note, got %q", p.LaneNote)
	}
}
