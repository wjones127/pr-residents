package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/wjones127/pr-residents/internal/agent"
	"github.com/wjones127/pr-residents/internal/prr"
	"github.com/wjones127/pr-residents/internal/relevance"
)

// RowView is one PR rendered in a lane. Tag is used only in housekeeping;
// Workup is populated when a review is cached for the PR's head SHA.
type RowView struct {
	Repo      string
	Number    int
	URL       string
	Title     string
	Risk      string // upper-cased
	Size      string
	CI        string
	CIClass   string
	Age       string
	Rationale string
	Tag       string
	Escalated bool
	Workup    *WorkupView
}

// WorkupView is a cached review rendered for display: the recommendation, the
// human summary, and the anchored draft-comment copy-cards.
type WorkupView struct {
	Recommendation string // approve | block | comment ("" = none)
	HeadLabel      string // e.g. "3 comments · 1 blocking"
	Summary        string
	SummaryCopyID  string
	Comments       []CommentCard
}

// CommentCard is one draft comment with a copy button and deep link.
type CommentCard struct {
	Label      string
	LabelClass string
	LabelDeco  string // " (blocking)" / " (non-blocking)" / ""
	LocText    string // "path:L123" or "review-level"
	DeepLink   string // "" when no path
	Body       string
	Suggestion string
	Blocking   bool
	CopyID     string
	CopyText   string
}

var labelClass = map[string]string{
	"issue": "l-issue", "suggestion": "l-suggestion", "question": "l-question",
	"nitpick": "l-nitpick", "praise": "l-praise", "todo": "l-todo",
	"thought": "l-thought", "chore": "l-chore",
}

func labelClassOf(label string) string {
	if c, ok := labelClass[label]; ok {
		return c
	}
	return "l-comment"
}

// diffAnchor is the GitHub Files-changed fragment for path[:line] — the same
// scheme GitHub's diff view uses: diff-<sha256(path)> + R<n> (new) / L<n> (old).
func diffAnchor(path string, line int, side string) string {
	sum := sha256.Sum256([]byte(path))
	frag := "diff-" + hex.EncodeToString(sum[:])
	if line > 0 {
		if strings.EqualFold(side, "LEFT") {
			frag += "L" + strconv.Itoa(line)
		} else {
			frag += "R" + strconv.Itoa(line)
		}
	}
	return frag
}

func labelDeco(c agent.DraftComment) string {
	if c.Label == "issue" || c.Label == "question" {
		if c.Blocking {
			return " (blocking)"
		}
		return " (non-blocking)"
	}
	return ""
}

// copyText is the exact markdown to paste into a GitHub review comment.
func copyText(c agent.DraftComment) string {
	head := "**" + c.Label + labelDeco(c) + ":**"
	out := head
	if b := strings.TrimSpace(c.Body); b != "" {
		out = head + " " + b
	}
	if c.Suggestion != "" {
		out += "\n\n```suggestion\n" + strings.TrimRight(c.Suggestion, "\n") + "\n```"
	}
	return out
}

func buildWorkupView(repo string, number int, doc agent.WorkupDoc) *WorkupView {
	summary := strings.TrimSpace(doc.Summary)
	if summary == "" {
		summary = strings.TrimSpace(doc.SOAP) // legacy free-text workup
	}
	if summary == "" && len(doc.Comments) == 0 {
		return nil
	}
	nb := 0
	for _, c := range doc.Comments {
		if c.Blocking && (c.Label == "issue" || c.Label == "question") {
			nb++
		}
	}
	head := "workup cached"
	if len(doc.Comments) > 0 {
		head = fmt.Sprintf("%d comment%s", len(doc.Comments), plural(len(doc.Comments)))
		if nb > 0 {
			head += fmt.Sprintf(" · %d blocking", nb)
		}
	}
	base := strings.ReplaceAll(fmt.Sprintf("%s-%d", repo, number), "/", "-")

	wv := &WorkupView{
		Recommendation: doc.Recommendation,
		HeadLabel:      head,
		Summary:        summary,
		SummaryCopyID:  "s-" + base,
	}
	for i, c := range doc.Comments {
		card := CommentCard{
			Label:      c.Label,
			LabelClass: labelClassOf(c.Label),
			LabelDeco:  labelDeco(c),
			Body:       strings.TrimSpace(c.Body),
			Suggestion: c.Suggestion,
			Blocking:   c.Blocking && (c.Label == "issue" || c.Label == "question"),
			CopyID:     fmt.Sprintf("c-%s-%d", base, i),
			CopyText:   copyText(c),
		}
		if c.Path != "" {
			loc := c.Path
			if c.Line > 0 {
				loc += fmt.Sprintf(":L%d", c.Line)
			}
			card.LocText = loc
			card.DeepLink = fmt.Sprintf("https://github.com/%s/pull/%d/files#%s", repo, number, diffAnchor(c.Path, c.Line, c.Side))
		} else {
			card.LocText = "review-level"
		}
		wv.Comments = append(wv.Comments, card)
	}
	return wv
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// TriageRow is one self-requested candidate in the triage panel.
type TriageRow struct {
	Score     string
	Repo      string
	Number    int
	URL       string
	Title     string
	Author    string
	Rationale string
}

// RepoLink points at a repo's GitHub PR search for finding review candidates by
// hand (the manual complement to the triage panel).
type RepoLink struct {
	Name string
	URL  string
}

// RoundsView is the whole page's data.
type RoundsView struct {
	DateLabel string
	Total     int
	Triage    []TriageRow
	RepoLinks []RepoLink
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

func laneRow(r *prr.Record, workups map[string]agent.WorkupDoc) RowView {
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
	if doc, ok := workups[key(r.Repo, r.Number)]; ok {
		row.Workup = buildWorkupView(r.Repo, r.Number, doc)
		// The resident's content-refined acuity replaces the path-only baseline
		// on the row (the baseline's "resident to refine" is a placeholder).
		if row.Workup != nil {
			if doc.Risk != "" {
				row.Risk = upper(doc.Risk)
			}
			if a := strings.TrimSpace(doc.Assessment); a != "" {
				row.Rationale = a
			}
		}
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
// attaches cached review copy-cards to fresh/re-review rows.
func BuildView(records []*prr.Record, workups map[string]agent.WorkupDoc, panel []relevance.Candidate, dateLabel string) RoundsView {
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
	for _, c := range panel {
		view.Triage = append(view.Triage, TriageRow{
			Score: fmt.Sprintf("%.1f", c.Score), Repo: c.Repo, Number: c.Number,
			URL: c.URL, Title: c.Title, Author: c.Author, Rationale: c.Rationale,
		})
	}
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
