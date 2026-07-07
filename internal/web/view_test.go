package web

import (
	"testing"

	"github.com/wjones127/pr-residents/internal/agent"
	"github.com/wjones127/pr-residents/internal/prr"
	"github.com/wjones127/pr-residents/internal/relevance"
)

func TestBuildWorkupViewLegacySOAP(t *testing.T) {
	// A workup cached in the old free-text `soap` shape (no summary/comments)
	// must still render — as the summary.
	doc := agent.WorkupDoc{Recommendation: "block", SOAP: "old free-text review body"}
	wv := buildWorkupView("o/r", 5, doc)
	if wv == nil {
		t.Fatal("legacy soap workup should render")
	}
	if wv.Summary != "old free-text review body" || wv.Recommendation != "block" {
		t.Errorf("legacy render: %+v", wv)
	}
	if wv.HeadLabel != "workup cached" {
		t.Errorf("head label for comment-less workup: %q", wv.HeadLabel)
	}
}

func TestBuildWorkupViewEmpty(t *testing.T) {
	if buildWorkupView("o/r", 5, agent.WorkupDoc{}) != nil {
		t.Error("an empty workup should not render")
	}
}

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

func TestBuildViewHouseSubsections(t *testing.T) {
	tru := true
	fal := false

	ready := rec("housekeeping", "low", "low", "green", 50)
	ready.BlockedOn = "merge"
	ready.MergeState.Mergeable = &tru // approved, mergeable, CI green -> ①

	conflict := rec("housekeeping", "low", "low", "green", 50)
	conflict.BlockedOn = "merge"
	conflict.MergeState.Mergeable = &fal // -> ② needs rebase

	stale := rec("housekeeping", "low", "low", "green", 50)
	stale.BlockedOn = "author"
	stale.Author = "carol"

	draft := rec("housekeeping", "low", "low", "green", 50)
	draft.BlockedOn = "author"
	draft.IsDraft = true
	draft.Author = "bob"

	view := BuildView([]*prr.Record{ready, conflict, stale, draft}, nil, nil, "d")

	if view.HouseCount() != 4 {
		t.Fatalf("expected 4 house rows total, got %d", view.HouseCount())
	}
	if len(view.HouseReady) != 1 || view.HouseReady[0].Tag != "ready" {
		t.Errorf("ready bucket: %+v", view.HouseReady)
	}
	if len(view.HouseNeeds) != 1 || view.HouseNeeds[0].Tag != "needs rebase (conflict)" {
		t.Errorf("needs-author bucket: %+v", view.HouseNeeds)
	}
	if len(view.HouseWaiting) != 2 {
		t.Fatalf("expected 2 waiting rows, got %d", len(view.HouseWaiting))
	}
	if view.HouseWaiting[0].Tag != "stale, waiting on carol" {
		t.Errorf("stale tag: %q", view.HouseWaiting[0].Tag)
	}
	if view.HouseWaiting[1].Tag != "draft, waiting on bob" {
		t.Errorf("draft tag: %q", view.HouseWaiting[1].Tag)
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
