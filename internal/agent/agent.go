package agent

import (
	"context"
	"strings"

	"github.com/lancedb/pr-residents/internal/config"
	"github.com/lancedb/pr-residents/internal/prr"
)

// DraftComment is one anchored review comment (the schema render turns into a
// copy-card + deep link). Matches config/comment-vocab.md conventional labels.
type DraftComment struct {
	Path       string `json:"path"`       // repo-relative; empty for a review-level comment
	Line       int    `json:"line"`       // head-side line; 0 for file/review-level
	Side       string `json:"side"`       // RIGHT (new, default) | LEFT (old)
	Label      string `json:"label"`      // issue | suggestion | question | nitpick | praise | todo | thought | chore
	Blocking   bool   `json:"blocking"`   // only meaningful for issue / question
	Body       string `json:"body"`       // the comment text, in the attending's voice
	Suggestion string `json:"suggestion"` // optional literal replacement -> ```suggestion``` block
}

// SOAP is a resident's review output: the human synthesis (Summary) plus the
// actionable, postable draft Comments. Tokens are filled from usage metadata.
type SOAP struct {
	Recommendation string // approve | block | comment
	Summary        string
	Comments       []DraftComment
	TokensIn       int
	TokensOut      int
}

// BlockingCount is the number of blocking issue/question comments.
func (s SOAP) BlockingCount() int {
	n := 0
	for _, c := range s.Comments {
		if c.Blocking && (c.Label == "issue" || c.Label == "question") {
			n++
		}
	}
	return n
}

// WorkupAgent produces a SOAP review from a fully-rendered prompt, using the
// given model (empty = the engine's default). Lane-agnostic: the caller renders
// the fresh or re-review prompt. The context cancels an in-flight run.
type WorkupAgent interface {
	Workup(ctx context.Context, prompt string, model string) (SOAP, error)
}

// ModelFor picks the model for a record from the routing table, trying the most
// specific key first: "<lane>_<size>", "<lane>", "<size>", "default". Empty
// result means "let the engine use its default".
func ModelFor(d config.Dispatch, r *prr.Record) string {
	size := strings.ToLower(r.Effort.SizeBucket)
	for _, k := range []string{r.Lane + "_" + size, r.Lane, size, "default"} {
		if m := d.ModelRouting[k]; m != "" {
			return m
		}
	}
	return ""
}
