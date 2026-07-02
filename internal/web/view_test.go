package web

import (
	"testing"

	"github.com/lancedb/pr-residents/internal/prr"
	"github.com/lancedb/pr-residents/internal/relevance"
)

func TestBuildViewTriage(t *testing.T) {
	panel := []relevance.Candidate{
		{Repo: "o/r", Number: 7, Title: "Cand", URL: "u", Author: "alice", Score: 5, Rationale: "overlaps"},
	}
	view := BuildView(nil, nil, panel, "d")
	if len(view.Triage) != 1 || view.Triage[0].Score != "5.0" || view.Triage[0].Number != 7 || view.Triage[0].Author != "alice" {
		t.Errorf("triage row: %+v", view.Triage)
	}
}

func rec(lane, risk, urgency, ci string, ageHrs float64) *prr.Record {
	return &prr.Record{
		Repo: "o/r", Number: 1, Title: "t", Lane: lane,
		Acuity:        prr.Acuity{Risk: risk, Urgency: urgency, Rationale: "why"},
		Effort:        prr.Effort{SizeBucket: "M"},
		MergeState:    prr.MergeState{CI: ci},
		AgeInStateHrs: ageHrs,
	}
}

func TestBuildViewFreshOrdering(t *testing.T) {
	med := rec("fresh", "med", "low", "green", 10)
	high := rec("fresh", "high", "low", "green", 10)
	view := BuildView([]*prr.Record{med, high}, nil, nil, "2026-07-02")
	if len(view.Fresh) != 2 || view.Fresh[0].Risk != "HIGH" {
		t.Errorf("fresh should be risk-ordered high first: %+v", view.Fresh)
	}
}

func TestBuildViewRereviewOrdering(t *testing.T) {
	red := rec("re_review", "med", "low", "red", 10)
	green := rec("re_review", "med", "low", "green", 10)
	view := BuildView([]*prr.Record{red, green}, nil, nil, "d")
	if len(view.Rereview) != 2 || view.Rereview[0].CI != "green" {
		t.Errorf("re-review should be CI-ordered green first: %+v", view.Rereview)
	}
}

func TestBuildViewHouseTags(t *testing.T) {
	draft := rec("housekeeping", "low", "low", "green", 50)
	draft.IsDraft = true
	draft.Author = "bob"
	merge := rec("housekeeping", "low", "low", "green", 50)
	merge.BlockedOn = "merge"
	stale := rec("housekeeping", "low", "low", "green", 50)
	stale.BlockedOn = "author"
	stale.Author = "carol"

	view := BuildView([]*prr.Record{draft, merge, stale}, nil, nil, "d")
	if len(view.House) != 3 {
		t.Fatalf("expected 3 house rows, got %d", len(view.House))
	}
	if view.House[0].Tag != "draft, waiting on bob" {
		t.Errorf("draft tag: %q", view.House[0].Tag)
	}
	if view.House[1].Tag != "approved, not merged" {
		t.Errorf("merge tag: %q", view.House[1].Tag)
	}
	if view.House[2].Tag != "stale, waiting on carol" {
		t.Errorf("stale tag: %q", view.House[2].Tag)
	}
}

func TestAgeLabel(t *testing.T) {
	if got := ageLabel(12); got != "12h" {
		t.Errorf("12h: %q", got)
	}
	if got := ageLabel(72); got != "3d" {
		t.Errorf("72h -> 3d: %q", got)
	}
}

func TestCIClass(t *testing.T) {
	for ci, want := range map[string]string{"green": "ci-green", "red": "ci-red", "pending": "ci-pending", "": "ci-pending"} {
		if got := ciClass(ci); got != want {
			t.Errorf("ciClass(%q)=%q want %q", ci, got, want)
		}
	}
}
