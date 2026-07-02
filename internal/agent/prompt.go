package agent

import (
	_ "embed"
	"encoding/json"
)

//go:embed prompts/fresh-review.md
var freshReviewPrompt string

// buildPrompt renders the full agent prompt: the review framework plus the
// packet as JSON.
func buildPrompt(p Packet) string {
	pj, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		pj = []byte("{}")
	}
	return freshReviewPrompt + "\n\n## Packet\n\n```json\n" + string(pj) + "\n```\n"
}
