package pipeline

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wjones127/pr-residents/internal/cache"
	"github.com/wjones127/pr-residents/internal/config"
	"github.com/wjones127/pr-residents/internal/gh"
)

var now = time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)

type fakeAPI struct {
	requested, reviewed []gh.LightPR
	details             map[int]*gh.Detail
	searchCount         int
	detailCount         int
}

func (f *fakeAPI) ViewerLogin() (string, error) { return "wjones127", nil }

func (f *fakeAPI) SearchLight(q string) ([]gh.LightPR, error) {
	if strings.Contains(q, "review-requested") {
		return f.requested, nil
	}
	return f.reviewed, nil
}

func (f *fakeAPI) SearchCount(q string) (int, error) {
	f.searchCount++
	return 5, nil
}

func (f *fakeAPI) FetchDetail(owner, name string, number int) (*gh.Detail, []string, error) {
	f.detailCount++
	return f.details[number], nil, nil
}

func det(number int, author string) *gh.Detail {
	oid := fmt.Sprintf("h%d", number)
	return &gh.Detail{
		Number: number, URL: "u", Title: "t",
		Author:     &gh.Actor{Login: author},
		HeadRefOid: oid, Mergeable: "MERGEABLE",
		Additions: 10, Deletions: 2, ChangedFiles: 1,
		Files: gh.Files{Nodes: []gh.FileNode{{Path: "rust/x.rs"}}},
		Commits: gh.Commits{Nodes: []gh.CommitNode{{Commit: gh.Commit{
			Oid: oid, CommittedDate: now.Add(-2 * time.Hour).Format(time.RFC3339),
			StatusCheckRollup: &gh.Rollup{State: "SUCCESS"},
		}}}},
	}
}

func light(number int, updatedAt string) gh.LightPR {
	return gh.LightPR{Repo: "o/r", Number: number, UpdatedAt: updatedAt, HeadRefOid: fmt.Sprintf("h%d", number)}
}

// reviewBy is a review left by the viewer, for building reviewed-lane details.
func reviewBy(state, oid string, hrsAgo float64) gh.Review {
	return gh.Review{
		Author: &gh.Actor{Login: "wjones127"}, State: state,
		SubmittedAt: now.Add(-time.Duration(hrsAgo * float64(time.Hour))).Format(time.RFC3339),
		Commit:      &gh.Commit{Oid: oid},
	}
}

// withReviews attaches reviews to a detail and returns it.
func withReviews(d *gh.Detail, rs ...gh.Review) *gh.Detail {
	d.Reviews.Nodes = rs
	return d
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("GITHUB_TOKEN_O", "tok")
	return &config.Config{Repos: []string{"o/r"}, TokenPrefix: "GITHUB_TOKEN"}
}

func TestSyncDedupAndRequested(t *testing.T) {
	cfg := testConfig(t)
	fake := &fakeAPI{
		// #2 is in both categories (dedup); reviewed PRs (#2, #3) carry a review
		// by me, else derive correctly drops them as other_reviewer.
		requested: []gh.LightPR{light(1, "u1"), light(2, "u2")},
		reviewed:  []gh.LightPR{light(2, "u2"), light(3, "u3")},
		details: map[int]*gh.Detail{
			1: det(1, "alice"),                                               // requested, never reviewed -> fresh
			2: withReviews(det(2, "bob"), reviewBy("COMMENTED", "old2", 10)), // head moved -> re_review
			3: withReviews(det(3, "carol"), reviewBy("APPROVED", "h3", 5)),   // approved at head -> housekeeping
		},
	}
	newClient := func(token string) API { return fake }

	records, warns := Sync(cfg, newClient, cache.NewMemory(), now)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(records) != 3 {
		t.Fatalf("expected 3 deduped records, got %d", len(records))
	}
	// Sorted by number.
	for i, want := range []int{1, 2, 3} {
		if records[i].Number != want {
			t.Errorf("records[%d].Number=%d want %d", i, records[i].Number, want)
		}
		if records[i].Repo != "o/r" || records[i].HeadOid != fmt.Sprintf("h%d", want) {
			t.Errorf("records[%d] repo/head: %+v", i, records[i])
		}
	}
	// Requested flags: 1,2 requested; 3 reviewed-only.
	if !records[0].Relevance.Requested || !records[1].Relevance.Requested || records[2].Relevance.Requested {
		t.Errorf("requested flags: %v %v %v",
			records[0].Relevance.Requested, records[1].Relevance.Requested, records[2].Relevance.Requested)
	}
	if records[0].Lane != "fresh" || records[1].Lane != "re_review" || records[2].Lane != "housekeeping" {
		t.Errorf("lanes: %s %s %s", records[0].Lane, records[1].Lane, records[2].Lane)
	}
	if records[0].AuthorStatus != "regular" { // count 5 -> regular
		t.Errorf("author_status: %s", records[0].AuthorStatus)
	}
	if fake.detailCount != 3 {
		t.Errorf("expected 3 detail fetches, got %d", fake.detailCount)
	}
	if fake.searchCount != 3 { // one merged-count per unique author
		t.Errorf("expected 3 merged-count searches, got %d", fake.searchCount)
	}
}

func TestSyncReusesCacheWhenUnchanged(t *testing.T) {
	cfg := testConfig(t)
	c := cache.NewMemory()

	first := &fakeAPI{
		requested: []gh.LightPR{light(1, "u1")},
		details:   map[int]*gh.Detail{1: det(1, "alice")},
	}
	Sync(cfg, func(string) API { return first }, c, now)

	// Same updatedAt and same requested-status -> pure cache hit, no re-fetch.
	second := &fakeAPI{
		requested: []gh.LightPR{light(1, "u1")},
		details:   map[int]*gh.Detail{}, // must not be consulted
	}
	records, _ := Sync(cfg, func(string) API { return second }, c, now)
	if second.detailCount != 0 {
		t.Errorf("unchanged PR should be a cache hit, got %d fetches", second.detailCount)
	}
	if len(records) != 1 || !records[0].Relevance.Requested {
		t.Errorf("cached record reused: %+v", records)
	}
}

func TestSyncReDerivesWhenRequestedFlips(t *testing.T) {
	cfg := testConfig(t)
	c := cache.NewMemory()

	// First run: #1 requested, never reviewed -> fresh, cached.
	first := &fakeAPI{
		requested: []gh.LightPR{light(1, "u1")},
		details:   map[int]*gh.Detail{1: det(1, "alice")},
	}
	Sync(cfg, func(string) API { return first }, c, now)

	// Second run: same updatedAt, but #1 is no longer requested and I've since
	// approved it -> must re-fetch and re-derive to housekeeping, not serve the
	// stale fresh record.
	second := &fakeAPI{
		reviewed: []gh.LightPR{light(1, "u1")},
		details:  map[int]*gh.Detail{1: withReviews(det(1, "alice"), reviewBy("APPROVED", "h1", 5))},
	}
	records, _ := Sync(cfg, func(string) API { return second }, c, now)
	if second.detailCount != 1 {
		t.Errorf("requested flip should force a re-fetch, got %d fetches", second.detailCount)
	}
	if len(records) != 1 || records[0].Lane != "housekeeping" || records[0].Relevance.Requested {
		t.Errorf("expected re-derived housekeeping record, got %+v", records)
	}
}

func TestSyncSkipsRepoWithoutToken(t *testing.T) {
	cfg := &config.Config{Repos: []string{"noauth/repo"}, TokenPrefix: "GITHUB_TOKEN"}
	records, warns := Sync(cfg, func(string) API { return &fakeAPI{} }, cache.NewMemory(), now)
	if len(records) != 0 {
		t.Errorf("expected no records, got %d", len(records))
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "not set") {
		t.Errorf("expected a skip warning, got %v", warns)
	}
}
