package web

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/lancedb/pr-residents/internal/prr"
)

// RowView is one PR rendered in a lane. Tag is used only in housekeeping;
// the SOAP fields are populated when a workup is cached for the PR's head SHA.
type RowView struct {
	Repo           string
	Number         int
	URL            string
	Title          string
	Risk           string // upper-cased
	Size           string
	CI             string
	CIClass        string
	Age            string
	Rationale      string
	Tag            string
	Escalated      bool
	HasSOAP        bool
	SOAP           string
	Recommendation string
	BlockingCount  int
}

// Workup is the display slice of a cached SOAP, keyed by "repo#number".
type Workup struct {
	SOAP           string
	Recommendation string
	BlockingCount  int
}

// RoundsView is the whole page's data.
type RoundsView struct {
	DateLabel string
	Total     int
	Fresh     []RowView
	Rereview  []RowView
	House     []RowView
}

var (
	riskRank    = map[string]int{"high": 0, "med": 1, "low": 2}
	urgencyRank = map[string]int{"high": 0, "med": 1, "low": 2}
	ciRank      = map[string]int{"green": 0, "pending": 1, "red": 2}
)

func rankOr(m map[string]int, k string) int {
	if v, ok := m[k]; ok {
		return v
	}
	return 3
}

func ageLabel(hrs float64) string {
	if hrs >= 48 {
		return fmt.Sprintf("%.0fd", hrs/24)
	}
	return fmt.Sprintf("%.0fh", hrs)
}

func ciClass(ci string) string {
	switch ci {
	case "green":
		return "ci-green"
	case "red":
		return "ci-red"
	default:
		return "ci-pending"
	}
}

func upper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 32
		}
	}
	return string(b)
}

func key(repo string, number int) string { return repo + "#" + strconv.Itoa(number) }

func laneRow(r *prr.Record, workups map[string]Workup) RowView {
	row := RowView{
		Repo:      r.Repo,
		Number:    r.Number,
		URL:       r.URL,
		Title:     r.Title,
		Risk:      upper(r.Acuity.Risk),
		Size:      r.Effort.SizeBucket,
		CI:        r.MergeState.CI,
		CIClass:   ciClass(r.MergeState.CI),
		Age:       ageLabel(r.AgeInStateHrs),
		Rationale: r.Acuity.Rationale,
		Escalated: r.Escalation.Forced,
	}
	if w, ok := workups[key(r.Repo, r.Number)]; ok && w.SOAP != "" {
		row.HasSOAP = true
		row.SOAP = w.SOAP
		row.Recommendation = w.Recommendation
		row.BlockingCount = w.BlockingCount
	}
	return row
}

func houseRow(r *prr.Record) RowView {
	var tag string
	switch {
	case r.IsDraft:
		tag = "draft, waiting on " + r.Author
	case r.BlockedOn == "merge":
		tag = "approved, not merged"
	default:
		tag = "stale, waiting on " + r.Author
	}
	row := laneRow(r, nil)
	row.Tag = tag
	return row
}

// BuildView sorts records into lanes and formats them for display, mirroring
// the Python render's lane ordering (fresh: acuity; re-review: proximity to
// merge; housekeeping: batched in input order). workups (keyed by "repo#number")
// attaches cached SOAP blocks to fresh/re-review rows.
func BuildView(records []*prr.Record, workups map[string]Workup, dateLabel string) RoundsView {
	var fresh, rereview, house []*prr.Record
	for _, r := range records {
		switch r.Lane {
		case "fresh":
			fresh = append(fresh, r)
		case "re_review":
			rereview = append(rereview, r)
		case "housekeeping":
			house = append(house, r)
		}
	}

	sort.SliceStable(fresh, func(i, j int) bool {
		ai, aj := fresh[i].Acuity, fresh[j].Acuity
		if r := rankOr(riskRank, ai.Risk) - rankOr(riskRank, aj.Risk); r != 0 {
			return r < 0
		}
		if u := rankOr(urgencyRank, ai.Urgency) - rankOr(urgencyRank, aj.Urgency); u != 0 {
			return u < 0
		}
		return fresh[i].AgeInStateHrs > fresh[j].AgeInStateHrs
	})
	sort.SliceStable(rereview, func(i, j int) bool {
		if c := rankOr(ciRank, rereview[i].MergeState.CI) - rankOr(ciRank, rereview[j].MergeState.CI); c != 0 {
			return c < 0
		}
		return rereview[i].AgeInStateHrs > rereview[j].AgeInStateHrs
	})

	view := RoundsView{DateLabel: dateLabel, Total: len(records)}
	for _, r := range fresh {
		view.Fresh = append(view.Fresh, laneRow(r, workups))
	}
	for _, r := range rereview {
		view.Rereview = append(view.Rereview, laneRow(r, workups))
	}
	for _, r := range house {
		view.House = append(view.House, houseRow(r))
	}
	return view
}
