package relevance

import (
	"strings"
	"testing"

	"github.com/lancedb/pr-residents/internal/config"
	"github.com/lancedb/pr-residents/internal/gh"
	"github.com/lancedb/pr-residents/internal/store"
)

type fakeAPI struct {
	reviewed   []gh.CandidatePR
	candidates []gh.CandidatePR
}

func (f *fakeAPI) ViewerLogin() (string, error) { return "me", nil }

func (f *fakeAPI) SearchWithFiles(q string, limit int) ([]gh.CandidatePR, error) {
	if strings.Contains(q, "draft:false") { // the candidate query
		return f.candidates, nil
	}
	return f.reviewed, nil // the reviewed-by history query
}

func cand(number int, author string, paths ...string) gh.CandidatePR {
	return gh.CandidatePR{Repo: "o/r", Number: number, Title: "t", URL: "u", Author: author, Paths: paths}
}

func testCfg(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("GITHUB_TOKEN_O", "tok")
	return &config.Config{Repos: []string{"o/r"}, TokenPrefix: "GITHUB_TOKEN"}
}

func TestBuildPanelAffinity(t *testing.T) {
	cfg := testCfg(t)
	// 5 reviewed PRs all in rust/lance-index -> profile weight 5, not cold.
	var reviewed []gh.CandidatePR
	for i := 0; i < MinHistory; i++ {
		reviewed = append(reviewed, cand(100+i, "x", "rust/lance-index/a.rs"))
	}
	fake := &fakeAPI{
		reviewed: reviewed,
		candidates: []gh.CandidatePR{
			cand(1, "alice", "rust/lance-index/x.rs"), // overlaps -> score 5
			cand(2, "bob", "java/Foo.java"),           // no overlap -> score 0, dropped
		},
	}
	panel, warns := BuildPanel(cfg, func(string) API { return fake }, store.New(t.TempDir()), Options{})
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	if len(panel) != 1 || panel[0].Number != 1 || panel[0].Mode != "affinity" || panel[0].Score != 5 {
		t.Fatalf("expected 1 affinity candidate scoring 5, got %+v", panel)
	}
}

func TestBuildPanelColdStart(t *testing.T) {
	cfg := testCfg(t)
	cfg.Interests = []string{"rust/lance-index"}
	fake := &fakeAPI{
		reviewed:   []gh.CandidatePR{cand(100, "x", "rust/lance-index/a.rs")}, // 1 < MinHistory -> cold
		candidates: []gh.CandidatePR{cand(1, "alice", "rust/lance-index/x.rs")},
	}
	panel, _ := BuildPanel(cfg, func(string) API { return fake }, store.New(t.TempDir()), Options{})
	if len(panel) != 1 || panel[0].Mode != "cold_start" || panel[0].Score != 1 {
		t.Fatalf("expected 1 cold-start candidate scoring 1, got %+v", panel)
	}
	if !strings.Contains(panel[0].Rationale, "interests") {
		t.Errorf("rationale should cite interests: %q", panel[0].Rationale)
	}
}

func TestBuildPanelExcludesClaimed(t *testing.T) {
	cfg := testCfg(t)
	cfg.Interests = []string{"rust/lance-index"}
	claimed := cand(1, "alice", "rust/lance-index/x.rs")
	claimed.Reviews = []gh.CandidateReview{{Login: "carol", State: "APPROVED"}} // another human has it
	fake := &fakeAPI{
		reviewed:   []gh.CandidatePR{cand(100, "x", "rust/lance-index/a.rs")},
		candidates: []gh.CandidatePR{claimed},
	}
	panel, _ := BuildPanel(cfg, func(string) API { return fake }, store.New(t.TempDir()), Options{})
	if len(panel) != 0 {
		t.Errorf("claimed PR should be excluded, got %+v", panel)
	}
}

func TestIsClaimed(t *testing.T) {
	me := "me"
	cases := []struct {
		name    string
		reviews []gh.CandidateReview
		want    bool
	}{
		{"human review", []gh.CandidateReview{{Login: "carol", State: "APPROVED"}}, true},
		{"only me", []gh.CandidateReview{{Login: "me", State: "APPROVED"}}, false},
		{"only a bot", []gh.CandidateReview{{Login: "coderabbit[bot]", State: "COMMENTED"}}, false},
		{"bot by type", []gh.CandidateReview{{Login: "ci", Type: "Bot", State: "COMMENTED"}}, false},
		{"dismissed", []gh.CandidateReview{{Login: "carol", State: "DISMISSED"}}, false},
		{"pending", []gh.CandidateReview{{Login: "carol", State: "PENDING"}}, false},
		{"none", nil, false},
	}
	for _, c := range cases {
		if got := isClaimed(c.reviews, me); got != c.want {
			t.Errorf("%s: isClaimed=%v want %v", c.name, got, c.want)
		}
	}
}
