package agent

import (
	_ "embed"
	"encoding/json"
)

//go:embed prompts/fresh-review.md
var freshReviewPrompt string

//go:embed prompts/re-review.md
var reReviewFramework string

func withPacket(framework string, packet any) string {
	pj, err := json.MarshalIndent(packet, "", "  ")
	if err != nil {
		pj = []byte("{}")
	}
	return framework + "\n\n## Packet\n\n```json\n" + string(pj) + "\n```\n"
}

// freshPrompt renders the fresh-review framework plus the packet as JSON.
func freshPrompt(p Packet) string { return withPacket(freshReviewPrompt, p) }

// reReviewPrompt renders the re-review framework plus the delta packet as JSON.
func reReviewPrompt(p ReReviewPacket) string { return withPacket(reReviewFramework, p) }
