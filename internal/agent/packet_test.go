package agent

import (
	"strings"
	"testing"

	"github.com/wjones127/pr-residents/internal/config"
	"github.com/wjones127/pr-residents/internal/gh"
	"github.com/wjones127/pr-residents/internal/prr"
)

type fakeFetcher struct {
	files   []gh.FileDiff
	err     error
	content map[string]string // path -> full head content
	issues  []gh.LinkedIssue
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

func (f fakeFetcher) FetchLinkedIssues(owner, name string, number int) ([]gh.LinkedIssue, error) {
	return f.issues, nil
}

func TestClassifyFile(t *testing.T) {
	cases := []struct {
		path   string
		reason string
		skip   bool
	}{
		{"Cargo.lock", "lockfile", true},
		{"python/uv.lock", "lockfile", true},                                  // Python projects everywhere
		{"nodejs/pnpm-lock.yaml", "lockfile", true},                           // lancedb nodejs
		{"src/enterprise/common/go.sum", "lockfile", true},                    // sophon go services
		{"datafusion/proto-common/src/generated/prost.rs", "generated", true}, // committed protobuf
		{"datafusion/proto-common/gen/src/main.rs", "", false},                // the generator itself is source
		{"vendor/foo/bar.go", "vendored", true},
		{"api/service.pb.go", "generated", true},
		{"rust/lance-core/src/lib.rs", "", false},
		{"benchmarks/sift/requirements.txt", "", false}, // dep spec, sometimes review-relevant
		{"datafusion/sqllogictest/test_files/join.slt", "", false},
		{"datafusion/core/tests/snapshots/plan.snap", "", false}, // snapshot diff is the review subject
		{"rust/lance/protos/format.proto", "", false},
	}
	rules := DefaultSkipRules()
	for _, c := range cases {
		reason, skip := rules.classify(c.path)
		if skip != c.skip || reason != c.reason {
			t.Errorf("classify(%q) = (%q, %v), want (%q, %v)", c.path, reason, skip, c.reason, c.skip)
		}
	}
}

func TestSkipRulesExtendFromConfig(t *testing.T) {
	cfg := &config.Config{Diff: config.Diff{Skip: config.DiffSkip{
		Lockfiles:      []string{"deno.lock"},
		VendoredPaths:  []string{"external"}, // no trailing slash -> normalized
		GeneratedPaths: []string{"autogen/"},
	}}}
	rules := skipRulesFromConfig(cfg)

	// Config entries are honored...
	for path, want := range map[string]string{
		"js/deno.lock":        "lockfile",
		"external/dep/x.rs":   "vendored",
		"a/autogen/b/plan.rs": "generated",
	} {
		if got, skip := rules.classify(path); !skip || got != want {
			t.Errorf("classify(%q) = (%q, %v), want (%q, true)", path, got, skip, want)
		}
	}
	// ...and the built-in defaults still apply (extend, not replace).
	if got, skip := rules.classify("Cargo.lock"); !skip || got != "lockfile" {
		t.Errorf("default Cargo.lock lost after extend: (%q, %v)", got, skip)
	}
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

	p, err := BuildPacket(ff, r, DefaultSkipRules())
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

func TestBuildPacketAttachesLinkedIssues(t *testing.T) {
	longBody := strings.Repeat("z", issueBodyMaxChars+500)
	longComment := strings.Repeat("c", issueCommentMaxChars+500)
	comments := make([]gh.IssueComment, maxIssueComments+5)
	for i := range comments {
		comments[i] = gh.IssueComment{Author: "alice", Body: "ok"}
	}
	comments[0] = gh.IssueComment{Author: "bob", Body: longComment}
	ff := fakeFetcher{
		files: []gh.FileDiff{{Filename: "a.go", Status: "modified", Patch: "@@ -1 +1 @@\n-x\n+y"}},
		issues: []gh.LinkedIssue{
			{Number: 42, Title: "bug", State: "OPEN", Body: longBody, Comments: comments},
		},
	}
	r := &prr.Record{Repo: "o/r", Number: 5, HeadOid: "abc", Lane: "fresh"}

	p, err := BuildPacket(ff, r, DefaultSkipRules())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.LinkedIssues) != 1 {
		t.Fatalf("want 1 linked issue, got %d", len(p.LinkedIssues))
	}
	is := p.LinkedIssues[0]
	if is.Number != 42 || is.Title != "bug" || is.State != "OPEN" {
		t.Errorf("issue identity wrong: %+v", is)
	}
	if !strings.Contains(is.Body, "…[truncated]") {
		t.Errorf("long body should be truncated")
	}
	if len([]rune(is.Comments[0].Body)) > issueCommentMaxChars+len([]rune("\n…[truncated]")) {
		t.Errorf("long comment not truncated: %d runes", len([]rune(is.Comments[0].Body)))
	}
	// capped at maxIssueComments kept + 1 "omitted" marker line
	if len(is.Comments) != maxIssueComments+1 {
		t.Fatalf("want %d comments (capped + marker), got %d", maxIssueComments+1, len(is.Comments))
	}
	if !strings.Contains(is.Comments[maxIssueComments].Body, "more comments omitted") {
		t.Errorf("expected omitted-comments marker, got %q", is.Comments[maxIssueComments].Body)
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

	p, err := BuildPacket(ff, r, DefaultSkipRules())
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

func TestAssembleDiffHardCapOmitsExcessPatches(t *testing.T) {
	patch := "@@ -1 +1 @@\n" + strings.Repeat("+x\n", 100)
	files := []gh.FileDiff{
		{Filename: "a.go", Status: "modified", Patch: patch},
		{Filename: "b.go", Status: "modified", Patch: patch},
	}
	b := budget{hardTokens: estTokens(patch) + 5} // room for exactly one patch
	shown, omitted := assembleDiff(files, nil, "o", "r", "head", b, DefaultSkipRules())
	if len(shown) != 1 || shown[0].Path != "a.go" {
		t.Fatalf("expected only a.go shown, got %+v", shown)
	}
	if r, ok := omittedReason(omitted, "b.go"); !ok || r != "budget" {
		t.Errorf("b.go should be omitted with reason budget, got %q found=%v", r, ok)
	}
}

func TestAssembleDiffSoftCapShedsFullContentFirst(t *testing.T) {
	patch := "@@ -1 +1 @@\n-a\n+b"
	small := "package x\nfunc A() {}\n"
	files := []gh.FileDiff{
		{Filename: "a.go", Status: "modified", Additions: 1, Deletions: 1, Patch: patch},
		{Filename: "b.go", Status: "modified", Additions: 1, Deletions: 1, Patch: patch},
	}
	cf := fakeFetcher{content: map[string]string{"a.go": small, "b.go": small}}
	// Soft cap sits between the first file's patch cost and the running total
	// after it: a.go gets full content, b.go's is shed while its patch stays.
	b := budget{softTokens: estTokens(patch) + estTokens(small), hardTokens: 100_000}
	shown, _ := assembleDiff(files, cf, "o", "r", "head", b, DefaultSkipRules())
	if len(shown) != 2 {
		t.Fatalf("both patches should be shown, got %d", len(shown))
	}
	if shown[0].FullContent == "" {
		t.Errorf("a.go should keep full content (under soft cap)")
	}
	if shown[1].FullContent != "" {
		t.Errorf("b.go full content should be shed past the soft cap")
	}
}

func TestBuildPacketLaneNoteForReReview(t *testing.T) {
	ff := fakeFetcher{files: []gh.FileDiff{{Filename: "a.go", Patch: "@@"}}}
	r := &prr.Record{Repo: "o/r", Number: 5, Lane: "re_review", BlockedOn: "me"}
	p, err := BuildPacket(ff, r, DefaultSkipRules())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.LaneNote, "re_review") {
		t.Errorf("expected a re_review lane note, got %q", p.LaneNote)
	}
}
