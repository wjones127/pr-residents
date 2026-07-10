// Package prr defines the PRRecord contract (see docs/prrecord.md): the single
// shape pr-sync produces and every downstream consumer reads. Field order here
// mirrors the Python producer's emit order for readable diffs; equality is
// compared semantically, so order is not load-bearing.
package prr

// Acuity is the risk/urgency pair with a one-line rationale.
type Acuity struct {
	Risk      string `json:"risk"`
	Urgency   string `json:"urgency"`
	Rationale string `json:"rationale"`
}

// Effort is the mechanical size axis (independent of risk).
type Effort struct {
	SizeBucket string `json:"size_bucket"`
	Additions  int    `json:"additions"`
	Deletions  int    `json:"deletions"`
	Files      int    `json:"files"`
}

// Relevance carries the triage score; Score is nil until the relevance pass
// runs, Requested is known from the timeline before any scoring.
type Relevance struct {
	Score     *float64 `json:"score"`
	Requested bool     `json:"requested"`
}

// Delta is the re_review scope: commits and files touched since last review.
// Nil outside the re_review lane.
type Delta struct {
	Commits      []string `json:"commits"`
	FilesTouched []string `json:"files_touched"`
}

// Condition is a reconstructed review condition (populated by re-review-delta,
// empty at sync time).
type Condition struct {
	ID             string `json:"id"`
	Text           string `json:"text"`
	Kind           string `json:"kind"`
	Status         string `json:"status"`
	EvidenceRef    string `json:"evidence_ref"`
	AuthorResolved bool   `json:"author_resolved"`
}

// MergeState is the CI rollup plus mergeability. Mergeable is nil when unknown.
// FailingChecks names the individual checks in a non-passing state so the
// resident can judge whether a red CI relates to the diff — it is context, not
// a review-blocking condition.
type MergeState struct {
	CI            string  `json:"ci"`
	Mergeable     *bool   `json:"mergeable"`
	FailingChecks []Check `json:"failing_checks,omitempty"`
}

// Check is one CI check in a non-passing state (Conclusion is the raw GitHub
// conclusion/state, e.g. FAILURE | TIMED_OUT | CANCELLED | ERROR).
type Check struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

// Escalation is the result of matching a PR against the bright-line rules.
type Escalation struct {
	Forced  bool     `json:"forced"`
	RuleIDs []string `json:"rule_ids"`
	Reason  string   `json:"reason"`
}

// Record is the PRRecord. LastReviewedSHA and Delta are pointers so they emit
// as JSON null (not absent) to match the Python producer.
type Record struct {
	Repo            string      `json:"repo"`
	Number          int         `json:"number"`
	URL             string      `json:"url"`
	Title           string      `json:"title"`
	Body            string      `json:"body"`
	Author          string      `json:"author"`
	AuthorStatus    string      `json:"author_status"`
	FilesChanged    []string    `json:"files_changed"`
	BlockedOn       string      `json:"blocked_on"`
	IsDraft         bool        `json:"is_draft"`
	AgeInStateHrs   float64     `json:"age_in_state_hrs"`
	Lane            string      `json:"lane"`
	Acuity          Acuity      `json:"acuity"`
	Effort          Effort      `json:"effort"`
	Relevance       Relevance   `json:"relevance"`
	LastReviewedSHA *string     `json:"last_reviewed_sha"`
	Delta           *Delta      `json:"delta"`
	Conditions      []Condition `json:"conditions"`
	ReviewDecision  string      `json:"review_decision"`
	MergeState      MergeState  `json:"merge_state"`
	Escalation      Escalation  `json:"escalation"`
	HeadOid         string      `json:"head_oid"`
}

// --- escalation rules (config/escalation.yml shape) -----------------------

type PathRule struct {
	ID             string   `json:"id" yaml:"id"`
	Reason         string   `json:"reason" yaml:"reason"`
	AnyPathMatches []string `json:"any_path_matches" yaml:"any_path_matches"`
}

type LabelRule struct {
	ID              string   `json:"id" yaml:"id"`
	Reason          string   `json:"reason" yaml:"reason"`
	AnyLabelMatches []string `json:"any_label_matches" yaml:"any_label_matches"`
}

type SizeRule struct {
	ID            string `json:"id" yaml:"id"`
	Reason        string `json:"reason" yaml:"reason"`
	MinTotalLines int    `json:"min_total_lines" yaml:"min_total_lines"`
}

// EscalationRules is the bright-line routing config.
type EscalationRules struct {
	PathRules  []PathRule  `json:"path_rules" yaml:"path_rules"`
	LabelRules []LabelRule `json:"label_rules" yaml:"label_rules"`
	SizeRules  []SizeRule  `json:"size_rules" yaml:"size_rules"`
}
