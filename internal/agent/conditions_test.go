package agent

import (
	"testing"

	"github.com/wjones127/pr-residents/internal/gh"
)

func TestParseLabel(t *testing.T) {
	cases := []struct {
		body           string
		wantLabel, dec string
		wantOK         bool
	}{
		{"issue(blocking): fix the leak", "issue", "blocking", true},
		{"suggestion: rename this", "suggestion", "", true},
		{"**issue (non-blocking):** later", "issue", "non-blocking", true},
		{"question: is this safe?", "question", "", true},
		{"just a plain comment", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		label, dec, ok := parseLabel(c.body)
		if ok != c.wantOK || label != c.wantLabel || dec != c.dec {
			t.Errorf("parseLabel(%q) = (%q,%q,%v) want (%q,%q,%v)", c.body, label, dec, ok, c.wantLabel, c.dec, c.wantOK)
		}
	}
}

func TestClassifyKind(t *testing.T) {
	cases := []struct{ label, dec, want string }{
		{"issue", "blocking", "blocking"},
		{"issue", "non-blocking", "non_blocking"},
		{"issue", "", "blocking"}, // bare issue -> conservative
		{"suggestion", "", "suggestion"},
		{"question", "", ""},
	}
	for _, c := range cases {
		if got := classifyKind(c.label, c.dec); got != c.want {
			t.Errorf("classifyKind(%q,%q)=%q want %q", c.label, c.dec, got, c.want)
		}
	}
}

func TestSubject(t *testing.T) {
	if got := subject("issue(blocking): fix the leak"); got != "fix the leak" {
		t.Errorf("subject: %q", got)
	}
}

func TestReconstructConditions(t *testing.T) {
	threads := []gh.ReviewThread{
		{ID: "t1", RootAuthor: "me", RootBody: "issue(blocking): guard the nil case", Path: "a.go", Line: 42, IsResolved: true},
		{ID: "t2", RootAuthor: "me", RootBody: "suggestion: extract a helper", Path: "b.go", Line: 5},
		{ID: "t3", RootAuthor: "me", RootBody: "question: why here?", Path: "c.go", Line: 1}, // not a condition
		{ID: "t4", RootAuthor: "someone-else", RootBody: "issue(blocking): not mine", Path: "d.go", Line: 9},
	}
	ledger := ReconstructConditions(threads, "me")
	if len(ledger) != 2 {
		t.Fatalf("expected 2 conditions, got %d: %+v", len(ledger), ledger)
	}
	if ledger[0].Kind != "blocking" || ledger[0].Text != "guard the nil case" || ledger[0].Location != "a.go:42" {
		t.Errorf("condition 0: %+v", ledger[0])
	}
	if !ledger[0].AuthorResolved || ledger[0].Status != "open" {
		t.Errorf("resolved-claim / open status: %+v", ledger[0])
	}
	if ledger[1].Kind != "suggestion" {
		t.Errorf("condition 1 kind: %+v", ledger[1])
	}
}
