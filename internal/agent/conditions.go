package agent

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/lancedb/pr-residents/internal/gh"
)

// Condition is one reconstructed review condition. Status starts "open"; the
// resident sets met/not_met/moot by verifying against the delta (trap #3:
// GitHub "resolved" is a claim, not a fact).
type Condition struct {
	ID             string `json:"id"`
	Text           string `json:"text"`
	Kind           string `json:"kind"` // blocking | non_blocking | suggestion
	Status         string `json:"status"`
	EvidenceRef    string `json:"evidence_ref"`
	Location       string `json:"location"`
	AuthorResolved bool   `json:"author_resolved"`
	IsOutdated     bool   `json:"is_outdated"`
}

// labelRE matches `label(decoration): subject`, tolerant of leading markdown.
var labelRE = regexp.MustCompile(
	`^[\s>*_~` + "`" + `]*(issue|suggestion|question|nitpick|praise|todo|thought)\s*(?:\(\s*([a-z -]+?)\s*\))?[\s*_]*:`)

var conditionLabels = map[string]bool{"issue": true, "suggestion": true}

// parseLabel returns (label, decoration) from a comment body, or ok=false.
func parseLabel(body string) (label, decoration string, ok bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", "", false
	}
	first := strings.ToLower(firstLine(body))
	m := labelRE.FindStringSubmatch(first)
	if m == nil {
		return "", "", false
	}
	return m[1], strings.TrimSpace(m[2]), true
}

// classifyKind maps a conventional label to a PRRecord condition kind, or "".
func classifyKind(label, decoration string) string {
	switch label {
	case "suggestion":
		return "suggestion"
	case "issue":
		if decoration == "non-blocking" {
			return "non_blocking"
		}
		return "blocking" // blocking, or bare `issue:` -> conservative
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// subject returns the comment's text after the label prefix.
func subject(body string) string {
	first := firstLine(strings.TrimSpace(body))
	if loc := labelRE.FindStringIndex(strings.ToLower(first)); loc != nil {
		return strings.TrimSpace(first[loc[1]:])
	}
	return first
}

// ReconstructConditions builds the conditions ledger from review threads whose
// root comment is authored by viewer (trap #4: reconstructed each run, never
// stored).
func ReconstructConditions(threads []gh.ReviewThread, viewer string) []Condition {
	ledger := []Condition{}
	for _, t := range threads {
		if t.RootAuthor != viewer {
			continue
		}
		label, decoration, ok := parseLabel(t.RootBody)
		if !ok || !conditionLabels[label] {
			continue
		}
		kind := classifyKind(label, decoration)
		if kind == "" {
			continue
		}
		line := t.Line
		if line == 0 {
			line = t.OriginalLine
		}
		var location string
		if t.Path != "" {
			location = t.Path + ":" + strconv.Itoa(line)
		}
		ledger = append(ledger, Condition{
			ID: t.ID, Text: subject(t.RootBody), Kind: kind, Status: "open",
			Location: location, AuthorResolved: t.IsResolved, IsOutdated: t.IsOutdated,
		})
	}
	return ledger
}
