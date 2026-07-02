package agent

import (
	"context"
	"strings"

	"github.com/lancedb/pr-residents/internal/config"
	"github.com/lancedb/pr-residents/internal/prr"
)

// SOAP is a resident's review output. Text is the full markdown review;
// Recommendation and BlockingCount are the machine-readable summary. Tokens are
// filled by the agent from usage metadata (not part of the JSON contract).
type SOAP struct {
	Text           string `json:"soap"`
	Recommendation string `json:"recommendation"` // approve | block | comment
	BlockingCount  int    `json:"blocking_count"`
	TokensIn       int    `json:"-"`
	TokensOut      int    `json:"-"`
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
