package derive

import (
	"testing"
	"time"

	"github.com/wjones127/pr-residents/internal/gh"
	"github.com/wjones127/pr-residents/internal/prr"
)

var (
	NOW  = time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	HEAD = "aaa111"
	OLD  = "bbb222"
)

var escalation = prr.EscalationRules{
	PathRules: []prr.PathRule{
		{ID: "secrets", Reason: "secret", AnyPathMatches: []string{"**/*secret*", "**/.env*"}},
		{ID: "public-api", Reason: "public api", AnyPathMatches: []string{"**/*.proto"}},
	},
	LabelRules: []prr.LabelRule{
		{ID: "breaking", Reason: "breaking", AnyLabelMatches: []string{"breaking-change"}},
	},
	SizeRules: []prr.SizeRule{
		{ID: "xl", Reason: "xl", MinTotalLines: 1000},
	},
}

func hoursAgo(h float64) string {
	return NOW.Add(-time.Duration(h * float64(time.Hour))).Format(time.RFC3339)
}

func baseDetail() *gh.Detail {
	return &gh.Detail{
		Number: 1, URL: "u", Title: "t",
		Author:     &gh.Actor{Login: "alice"},
		HeadRefOid: HEAD, Mergeable: "MERGEABLE",
		Additions: 20, Deletions: 5, ChangedFiles: 2,
		Labels:  gh.Labels{Nodes: []gh.Label{}},
		Files:   gh.Files{Nodes: []gh.FileNode{{Path: "rust/src/foo.rs"}}},
		Reviews: gh.Reviews{Nodes: []gh.Review{}},
		Commits: gh.Commits{Nodes: []gh.CommitNode{{Commit: gh.Commit{
			Oid: HEAD, CommittedDate: hoursAgo(2), StatusCheckRollup: &gh.Rollup{State: "SUCCESS"},
		}}}},
		TimelineItems: gh.Timeline{Nodes: []gh.TimelineNode{}},
	}
}

func review(state, oid string, hrs float64) gh.Review {
	return gh.Review{
		Author: &gh.Actor{Login: "wjones127"}, State: state,
		SubmittedAt: hoursAgo(hrs), Commit: &gh.Commit{Oid: oid},
	}
}

func intptr(n int) *int { return &n }

func TestGlob(t *testing.T) {
	if !PathMatches("a/b/secret.rs", "**/*secret*") {
		t.Error("expected a/b/secret.rs to match **/*secret*")
	}
	if !PathMatches("secret.txt", "**/*secret*") {
		t.Error("expected secret.txt to match **/*secret*")
	}
	if PathMatches("safe.rs", "**/*secret*") {
		t.Error("safe.rs should not match **/*secret*")
	}
	if !PathMatches("a/x.proto", "**/*.proto") {
		t.Error("expected a/x.proto to match **/*.proto")
	}
	if PathMatches("a/x.protobuf", "**/*.proto") {
		t.Error("a/x.protobuf should not match **/*.proto")
	}
}

func TestEscalation(t *testing.T) {
	e := MatchEscalation([]string{"src/secret_store.rs"}, nil, 10, escalation)
	if !e.Forced || !contains(e.RuleIDs, "secrets") {
		t.Errorf("secrets path rule: got %+v", e)
	}
	e = MatchEscalation([]string{"a.rs"}, []string{"breaking-change"}, 10, escalation)
	if !e.Forced || !contains(e.RuleIDs, "breaking") {
		t.Errorf("breaking label rule: got %+v", e)
	}
	e = MatchEscalation([]string{"a.rs"}, nil, 1500, escalation)
	if !contains(e.RuleIDs, "xl") {
		t.Errorf("xl size rule: got %+v", e)
	}
	e = MatchEscalation([]string{"a.rs"}, nil, 10, escalation)
	if e.Forced {
		t.Errorf("no match expected, got %+v", e)
	}
}

func TestEffort(t *testing.T) {
	cases := []struct {
		add, del, files int
		want            string
	}{
		{5, 3, 1, "XS"}, {30, 10, 2, "S"}, {150, 40, 5, "M"},
		{400, 150, 9, "L"}, {900, 200, 30, "XL"},
	}
	for _, c := range cases {
		if got := DeriveEffort(c.add, c.del, c.files).SizeBucket; got != c.want {
			t.Errorf("DeriveEffort(%d,%d)=%s want %s", c.add, c.del, got, c.want)
		}
	}
}

func TestBlockedOn(t *testing.T) {
	bo, _ := DeriveBlockedOn(baseDetail(), "wjones127", true)
	if bo != "me" {
		t.Errorf("never-reviewed requested: got %s", bo)
	}
	bo, _ = DeriveBlockedOn(baseDetail(), "wjones127", false)
	if bo != "other_reviewer" {
		t.Errorf("never-reviewed not-requested: got %s", bo)
	}

	d := baseDetail()
	d.Reviews.Nodes = []gh.Review{review("APPROVED", HEAD, 5)}
	if bo, _ = DeriveBlockedOn(d, "wjones127", false); bo != "merge" {
		t.Errorf("approved current head: got %s", bo)
	}

	d = baseDetail()
	d.Reviews.Nodes = []gh.Review{review("CHANGES_REQUESTED", HEAD, 5)}
	if bo, _ = DeriveBlockedOn(d, "wjones127", false); bo != "author" {
		t.Errorf("changes-requested no push: got %s", bo)
	}

	d = baseDetail()
	d.Reviews.Nodes = []gh.Review{review("APPROVED", OLD, 5)}
	if bo, _ = DeriveBlockedOn(d, "wjones127", false); bo != "me" {
		t.Errorf("author pushed since approval: got %s", bo)
	}

	d = baseDetail()
	d.IsDraft = true
	if bo, _ = DeriveBlockedOn(d, "wjones127", true); bo != "author" {
		t.Errorf("draft even when requested: got %s", bo)
	}

	d = baseDetail()
	d.IsDraft = true
	d.Reviews.Nodes = []gh.Review{review("COMMENTED", OLD, 10)}
	if bo, _ = DeriveBlockedOn(d, "wjones127", true); bo != "author" {
		t.Errorf("draft even after my review: got %s", bo)
	}
}

func TestBuildRecord(t *testing.T) {
	rec := BuildRecord(baseDetail(), "wjones127", true, escalation, NOW, nil)
	if rec.Lane != "fresh" || rec.BlockedOn != "me" || rec.Delta != nil {
		t.Errorf("fresh lane: %+v", rec)
	}

	d := baseDetail()
	d.Reviews.Nodes = []gh.Review{review("COMMENTED", OLD, 10)}
	rec = BuildRecord(d, "wjones127", true, escalation, NOW, nil)
	if rec.Lane != "re_review" || rec.LastReviewedSHA == nil || *rec.LastReviewedSHA != OLD || rec.Delta == nil {
		t.Errorf("re_review lane: %+v", rec)
	}

	d = baseDetail()
	d.Reviews.Nodes = []gh.Review{review("APPROVED", HEAD, 5)}
	rec = BuildRecord(d, "wjones127", false, escalation, NOW, nil)
	if rec.Lane != "housekeeping" || rec.BlockedOn != "merge" {
		t.Errorf("housekeeping merge: %+v", rec)
	}

	// Stale draft (>48h) lands in housekeeping, not re_review.
	d = baseDetail()
	d.IsDraft = true
	d.Reviews.Nodes = []gh.Review{review("COMMENTED", OLD, 72)}
	d.Commits.Nodes = []gh.CommitNode{{Commit: gh.Commit{Oid: HEAD, CommittedDate: hoursAgo(72), StatusCheckRollup: &gh.Rollup{State: "SUCCESS"}}}}
	rec = BuildRecord(d, "wjones127", true, escalation, NOW, nil)
	if rec == nil || rec.Lane != "housekeeping" || !rec.IsDraft {
		t.Errorf("stale draft housekeeping: %+v", rec)
	}

	// Fresh draft I'm requested on isn't ready -> dropped.
	d = baseDetail()
	d.IsDraft = true
	if rec = BuildRecord(d, "wjones127", true, escalation, NOW, nil); rec != nil {
		t.Errorf("fresh draft should be dropped, got %+v", rec)
	}

	// A PR I authored is never surfaced.
	d = baseDetail()
	d.Author = &gh.Actor{Login: "wjones127"}
	d.Reviews.Nodes = []gh.Review{review("APPROVED", HEAD, 5)}
	if rec = BuildRecord(d, "wjones127", true, escalation, NOW, nil); rec != nil {
		t.Errorf("own PR should be dropped, got %+v", rec)
	}

	// other_reviewer dropped.
	if rec = BuildRecord(baseDetail(), "wjones127", false, escalation, NOW, nil); rec != nil {
		t.Errorf("other_reviewer should be dropped, got %+v", rec)
	}

	// Escalation does not collapse re_review.
	d = baseDetail()
	d.Files.Nodes = []gh.FileNode{{Path: "proto/format.proto"}}
	d.Reviews.Nodes = []gh.Review{review("COMMENTED", OLD, 10)}
	rec = BuildRecord(d, "wjones127", true, escalation, NOW, nil)
	if !rec.Escalation.Forced || rec.Lane != "re_review" || rec.Delta == nil {
		t.Errorf("escalation must not collapse re_review: %+v", rec)
	}

	// Escalation decoupled from risk: label-escalated docs-only stays low risk.
	d = baseDetail()
	d.Files.Nodes = []gh.FileNode{{Path: "docs/guide.md"}}
	d.Labels.Nodes = []gh.Label{{Name: "breaking-change"}}
	rec = BuildRecord(d, "wjones127", true, escalation, NOW, nil)
	if !rec.Escalation.Forced || rec.Acuity.Risk != "low" {
		t.Errorf("escalation decoupled from risk: %+v", rec)
	}

	// Low-risk docs-only.
	d = baseDetail()
	d.Files.Nodes = []gh.FileNode{{Path: "docs/guide.md"}, {Path: "README.md"}}
	rec = BuildRecord(d, "wjones127", true, escalation, NOW, nil)
	if rec.Acuity.Risk != "low" {
		t.Errorf("docs-only low risk: %+v", rec)
	}
}

func TestHousekeepingBucket(t *testing.T) {
	// Approved-head PR (baseDetail: mergeable, CI green, no required review).
	approved := func() *gh.Detail {
		d := baseDetail()
		d.Reviews.Nodes = []gh.Review{review("APPROVED", HEAD, 5)}
		return d
	}
	build := func(d *gh.Detail) *prr.Record {
		return BuildRecord(d, "wjones127", false, escalation, NOW, nil)
	}

	if b := HousekeepingBucket(build(approved())); b != "ready" {
		t.Errorf("approved+green+mergeable should be ready, got %q", b)
	}

	d := approved()
	d.ReviewDecision = "APPROVED"
	rec := build(d)
	if b := HousekeepingBucket(rec); b != "ready" {
		t.Errorf("reviewDecision APPROVED should be ready, got %q", b)
	}
	if rec.ReviewDecision != "APPROVED" {
		t.Errorf("record should carry review_decision, got %q", rec.ReviewDecision)
	}

	d = approved()
	d.Mergeable = "CONFLICTING"
	if b := HousekeepingBucket(build(d)); b != "needs_author" {
		t.Errorf("conflict should be needs_author, got %q", b)
	}

	d = approved()
	d.Commits.Nodes[0].Commit.StatusCheckRollup = &gh.Rollup{State: "FAILURE"}
	if b := HousekeepingBucket(build(d)); b != "needs_author" {
		t.Errorf("red CI should be needs_author, got %q", b)
	}

	d = approved()
	d.Commits.Nodes[0].Commit.StatusCheckRollup = &gh.Rollup{State: "PENDING"}
	if b := HousekeepingBucket(build(d)); b != "needs_author" {
		t.Errorf("pending CI should be needs_author, got %q", b)
	}

	d = approved()
	d.ReviewDecision = "REVIEW_REQUIRED"
	if b := HousekeepingBucket(build(d)); b != "needs_author" {
		t.Errorf("review-required should be needs_author, got %q", b)
	}

	// Stale changes-requested -> author's court.
	d = baseDetail()
	d.Reviews.Nodes = []gh.Review{review("CHANGES_REQUESTED", HEAD, 72)}
	if b := HousekeepingBucket(build(d)); b != "stale_author" {
		t.Errorf("stale changes-requested should be stale_author, got %q", b)
	}

	// Parked stale draft.
	d = baseDetail()
	d.IsDraft = true
	d.Reviews.Nodes = []gh.Review{review("COMMENTED", OLD, 72)}
	d.Commits.Nodes = []gh.CommitNode{{Commit: gh.Commit{Oid: HEAD, CommittedDate: hoursAgo(72), StatusCheckRollup: &gh.Rollup{State: "SUCCESS"}}}}
	if b := HousekeepingBucket(build(d)); b != "stale_author" {
		t.Errorf("stale draft should be stale_author, got %q", b)
	}
}

func TestAuthorStatus(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{{0, "first_time"}, {1, "infrequent"}, {3, "infrequent"}, {4, "regular"}, {10, "regular"}, {11, "core"}}
	for _, c := range cases {
		if got := DeriveAuthorStatus(intptr(c.n)); got != c.want {
			t.Errorf("DeriveAuthorStatus(%d)=%s want %s", c.n, got, c.want)
		}
	}
	if got := DeriveAuthorStatus(nil); got != "unknown" {
		t.Errorf("nil count should be unknown, got %s", got)
	}
}

func TestTriageFields(t *testing.T) {
	d := baseDetail()
	d.Body = "Fixes #123"
	d.Files.Nodes = []gh.FileNode{{Path: "rust/src/foo.rs"}, {Path: "docs/x.md"}}
	rec := BuildRecord(d, "wjones127", true, escalation, NOW, intptr(0))
	if rec.Body != "Fixes #123" || rec.AuthorStatus != "first_time" {
		t.Errorf("triage inputs: %+v", rec)
	}
	if len(rec.FilesChanged) != 2 || rec.FilesChanged[0] != "rust/src/foo.rs" || rec.FilesChanged[1] != "docs/x.md" {
		t.Errorf("files_changed: %+v", rec.FilesChanged)
	}

	// build_record without a count must not fabricate a status.
	rec = BuildRecord(baseDetail(), "wjones127", true, escalation, NOW, nil)
	if rec.AuthorStatus != "unknown" {
		t.Errorf("author_status without count should be unknown, got %s", rec.AuthorStatus)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
