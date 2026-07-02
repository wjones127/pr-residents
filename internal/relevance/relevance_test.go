package relevance

import (
	"reflect"
	"testing"
)

const d = DefaultBucketDepth

func TestBucketPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"rust/lance-encoding/src/x.rs", "rust/lance-encoding"},
		{"python/python/lance/arrow.py", "python/python"},
		{"README.md", "README.md"},
		{"docs/guide.md", "docs"},
		{"", ""},
	}
	for _, c := range cases {
		if got := BucketPath(c.in, d); got != c.want {
			t.Errorf("BucketPath(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestBuildProfile(t *testing.T) {
	reviewed := [][]string{
		{"rust/lance-encoding/a.rs", "rust/lance-encoding/b.rs"},
		{"rust/lance-encoding/c.rs", "rust/lance-index/d.rs"},
		{"python/python/lance/x.py"},
	}
	prof := BuildProfile(reviewed, d)
	if prof["rust/lance-encoding"] != 2 || prof["rust/lance-index"] != 1 || prof["python/python"] != 1 {
		t.Errorf("profile counts PRs not files: %+v", prof)
	}
	if len(BuildProfile(nil, d)) != 0 {
		t.Error("empty history should be empty profile")
	}
}

func TestScoreCandidate(t *testing.T) {
	profile := map[string]int{"rust/lance-encoding": 14, "rust/lance-index": 6, "python/python": 2}

	score, matched := ScoreCandidate([]string{"rust/lance-encoding/src/x.rs", "rust/lance-index/y.rs"}, profile, d)
	if score != 20.0 {
		t.Errorf("score=%v want 20", score)
	}
	want := []Match{{"rust/lance-encoding", 14}, {"rust/lance-index", 6}}
	if !reflect.DeepEqual(matched, want) {
		t.Errorf("matched=%+v want %+v", matched, want)
	}

	score, matched = ScoreCandidate([]string{"java/Foo.java"}, profile, d)
	if score != 0.0 || len(matched) != 0 {
		t.Errorf("no overlap: score=%v matched=%+v", score, matched)
	}

	_, matched = ScoreCandidate([]string{"python/python/x.py", "rust/lance-encoding/y.rs"}, profile, d)
	if len(matched) != 2 || matched[0].Bucket != "rust/lance-encoding" || matched[1].Bucket != "python/python" {
		t.Errorf("matched sort by weight desc: %+v", matched)
	}
}

func TestAffinityRationale(t *testing.T) {
	profile := map[string]int{"rust/lance-encoding": 14}
	_, matched := ScoreCandidate([]string{"rust/lance-encoding/x.rs"}, profile, d)
	if got := AffinityRationale(matched); got != "overlaps your review history: rust/lance-encoding (reviewed 14×)" {
		t.Errorf("rationale: %q", got)
	}
	if got := AffinityRationale(nil); got != "no overlap with areas you've reviewed" {
		t.Errorf("empty rationale: %q", got)
	}
}

func TestColdStart(t *testing.T) {
	score, why := ColdStartScore(
		[]string{"rust/lance-encoding/src/x.rs", "docs/y.md"},
		[]string{"rust/lance-encoding"}, nil)
	if score != 1.0 || !contains(why, "matches your interests: rust/lance-encoding") || !contains(why, "cold-start") {
		t.Errorf("interest prefix match: score=%v why=%q", score, why)
	}

	// prefix, not bare substring.
	score, _ = ColdStartScore([]string{"rust/lance-encoding/x.rs"}, []string{"rust/lance-enc"}, nil)
	if score != 0.0 {
		t.Errorf("interest must match on segment boundary, got %v", score)
	}

	score, why = ColdStartScore([]string{"protos/format.proto"}, nil, []string{"public-api"})
	if score != 1.0 || !contains(why, "escalation paths: public-api") {
		t.Errorf("escalation adds to score: score=%v why=%q", score, why)
	}

	score, why = ColdStartScore([]string{"x/y.rs"}, nil, nil)
	if score != 0.0 || !contains(why, "no shared-signal match") {
		t.Errorf("no signal: score=%v why=%q", score, why)
	}
}

func TestPrimaryDomain(t *testing.T) {
	if got := PrimaryDomain([]string{"rust/lance-index/a.rs", "rust/lance-index/b.rs", "rust/lance-core/c.rs"}, d); got != "rust/lance-index" {
		t.Errorf("dominant bucket: %q", got)
	}
	if got := PrimaryDomain([]string{"rust/lance-index/a.rs", "rust/lance-core/b.rs"}, d); got != "rust/lance-core" {
		t.Errorf("tie breaks alphabetically: %q", got)
	}
	if got := PrimaryDomain([]string{"python/python/lancedb/x.py", "python/python/lancedb/y.py"}, d); got != "python/python" {
		t.Errorf("bucket depth: %q", got)
	}
	if PrimaryDomain(nil, d) != "" {
		t.Error("empty should be empty")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
