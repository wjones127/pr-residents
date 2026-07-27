package agent

import (
	"strings"
	"testing"

	"github.com/wjones127/pr-residents/internal/gh"
	"github.com/wjones127/pr-residents/internal/prr"
)

type fakeFetcher struct {
	files   []gh.FileDiff
	err     error
	content map[string]string // path -> full head content
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

func (f fakeFetcher) FileContent(owner, name, path, ref string) (string, error) {
	return f.content[path], nil
}

func omittedReason(omitted []OmittedFile, path string) (string, bool) {
	for _, o := range omitted {
		if o.Path == path {
			return o.Reason, true
		}
	}
	return "", false
}

func TestBuildPacketPartitionsAndTruncates(t *testing.T) {
	big := "@@ -1 +1 @@\n" + strings.Repeat("+x\n", maxPatchLines+50)
	ff := fakeFetcher{files: []gh.FileDiff{
		{Filename: "a.go", Status: "modified", Additions: 3, Deletions: 1, Patch: "@@ -1 +1 @@\n-x\n+y"},
		{Filename: "img.png", Status: "added"},                             // no patch -> omitted
		{Filename: "package-lock.json", Status: "modified", Patch: "@@ x"}, // junk -> omitted
		{Filename: "api/service.pb.go", Status: "modified", Patch: "@@ y"}, // generated -> omitted
		{Filename: "vendor/lib/x.go", Status: "modified", Patch: "@@ z"},   // vendored -> omitted
		{Filename: "big.txt", Status: "modified", Patch: big},              // real, oversized -> truncated
	}}
	r := &prr.Record{Repo: "o/r", Number: 5, Title: "t", URL: "u", HeadOid: "abc", Lane: "fresh"}

	p, err := BuildPacket(ff, r)
	if err != nil {
		t.Fatal(err)
	}
	if p.Diff.TotalFiles != 6 || p.Diff.ShownFiles != 2 {
		t.Errorf("counts: total=%d shown=%d", p.Diff.TotalFiles, p.Diff.ShownFiles)
	}
	for path, want := range map[string]string{
		"img.png":           "no-patch",
		"package-lock.json": "lockfile",
		"api/service.pb.go": "generated",
		"vendor/lib/x.go":   "vendored",
	} {
		if got, ok := omittedReason(p.Diff.Omitted, path); !ok || got != want {
			t.Errorf("omitted[%s] = %q (found=%v), want %q", path, got, ok, want)
		}
	}
	var bigFile *PacketDiffFile
	for i := range p.Diff.Files {
		if p.Diff.Files[i].Path == "big.txt" {
			bigFile = &p.Diff.Files[i]
		}
	}
	if bigFile == nil || !bigFile.PatchTruncated {
		t.Fatalf("big.txt should be truncated: %+v", bigFile)
	}
	if strings.Count(bigFile.Patch, "\n") >= maxPatchLines+1 {
		t.Errorf("patch not truncated to %d lines", maxPatchLines)
	}
	if !strings.Contains(bigFile.Patch, "elided to fit review budget") {
		t.Errorf("truncated patch should carry an elision marker: %q", bigFile.Patch)
	}
	if p.LaneNote != "" {
		t.Errorf("fresh lane should have no lane note, got %q", p.LaneNote)
	}
}

func TestBuildPacketAttachesFullContentForSmallFiles(t *testing.T) {
	small := "package x\n\nfunc A() {}\n"                  // 3 lines, modified -> attached
	huge := strings.Repeat("line\n", smallFileMaxLines+10) // exceeds cap -> not attached
	ff := fakeFetcher{
		files: []gh.FileDiff{
			{Filename: "small.go", Status: "modified", Additions: 2, Deletions: 1, Patch: "@@ -1 +1 @@\n-a\n+b"},
			{Filename: "big.go", Status: "modified", Additions: 5, Deletions: 1, Patch: "@@ -1 +1 @@\n-a\n+b"},
			{Filename: "new.go", Status: "added", Additions: 2, Patch: "@@ -0,0 +1,2 @@\n+a\n+b"},
		},
		content: map[string]string{"small.go": small, "big.go": huge, "new.go": "irrelevant\n"},
	}
	r := &prr.Record{Repo: "o/r", Number: 5, HeadOid: "abc", Lane: "fresh"}

	p, err := BuildPacket(ff, r)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]PacketDiffFile{}
	for _, f := range p.Diff.Files {
		byPath[f.Path] = f
	}
	if got := byPath["small.go"]; got.FullContent != small || got.FullContentLines != 3 {
		t.Errorf("small.go should carry full content (3 lines): content=%q lines=%d", got.FullContent, got.FullContentLines)
	}
	if got := byPath["big.go"]; got.FullContent != "" {
		t.Errorf("big.go exceeds cap; should not carry full content, got %d bytes", len(got.FullContent))
	}
	if got := byPath["new.go"]; got.FullContent != "" {
		t.Errorf("added file's patch is already the whole file; no full content expected")
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
