package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Runner runs a command with stdin and returns its stdout. Injected so the
// agent is testable without invoking the real `claude` binary.
type Runner func(ctx context.Context, name string, args []string, stdin string) ([]byte, error)

// ClaudeAgent runs reviews via headless Claude Code (`claude -p`), using the
// CLI's own subscription auth. It never uses an API key.
type ClaudeAgent struct {
	Bin string
	Run Runner
}

// NewClaudeAgent returns an agent that shells out to `claude` on PATH.
func NewClaudeAgent() *ClaudeAgent {
	return &ClaudeAgent{Bin: "claude", Run: execRunner}
}

func execRunner(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// claudeEnvelope is the `claude -p --output-format json` result shape (subset).
type claudeEnvelope struct {
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Workup runs one review. The prompt is passed on stdin (large), output is the
// JSON SOAP contract embedded in the CLI's result envelope.
func (a *ClaudeAgent) Workup(ctx context.Context, prompt string, model string) (SOAP, error) {
	args := []string{"-p", "--output-format", "json"}
	if model != "" {
		args = append(args, "--model", model)
	}
	out, err := a.Run(ctx, a.Bin, args, prompt)
	if err != nil {
		return SOAP{}, err
	}

	var env claudeEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return SOAP{}, fmt.Errorf("parse claude envelope: %w", err)
	}
	if env.IsError {
		return SOAP{}, fmt.Errorf("claude reported an error: %s", env.Result)
	}

	soap, err := parseSOAP(env.Result)
	if err != nil {
		return SOAP{}, err
	}
	soap.TokensIn = env.Usage.InputTokens
	soap.TokensOut = env.Usage.OutputTokens
	return soap, nil
}

// The agent output is framed, not one big JSON object: a RECOMMENDATION header,
// the free-form SUMMARY prose, then COMMENTS as one compact JSON object per line
// (JSONL). This keeps the multi-line prose out of JSON (what broke JSON-in-JSON)
// and lets a single malformed comment line be skipped instead of failing the
// whole review.
const (
	summaryDelim  = "===SUMMARY==="
	commentsDelim = "===COMMENTS==="
)

// parseSOAP reads the framed agent output. Deliberately lenient: missing
// sections or bad comment lines never discard the rest.
func parseSOAP(result string) (SOAP, error) {
	text := strings.TrimSpace(result)
	if text == "" {
		return SOAP{}, fmt.Errorf("empty agent output")
	}

	var s SOAP

	// RECOMMENDATION from the header (before the first delimiter).
	headerEnd := len(text)
	for _, d := range []string{summaryDelim, commentsDelim} {
		if i := strings.Index(text, d); i >= 0 && i < headerEnd {
			headerEnd = i
		}
	}
	for _, line := range strings.Split(text[:headerEnd], "\n") {
		if rec := parseRecommendation(line); rec != "" {
			s.Recommendation = rec
		}
	}

	// SUMMARY between its delimiter and COMMENTS (or end).
	if i := strings.Index(text, summaryDelim); i >= 0 {
		rest := text[i+len(summaryDelim):]
		if j := strings.Index(rest, commentsDelim); j >= 0 {
			rest = rest[:j]
		}
		s.Summary = strings.TrimSpace(rest)
	}

	// COMMENTS as JSONL (one object per line); fall back to a JSON array.
	if i := strings.Index(text, commentsDelim); i >= 0 {
		section := text[i+len(commentsDelim):]
		for _, line := range strings.Split(section, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "{") {
				continue
			}
			var c DraftComment
			if err := json.Unmarshal([]byte(line), &c); err != nil {
				continue // skip a malformed line, keep the rest
			}
			s.Comments = append(s.Comments, normalizeComment(c))
		}
		if len(s.Comments) == 0 {
			if arr := strings.TrimSpace(section); strings.HasPrefix(arr, "[") {
				var cs []DraftComment
				if json.Unmarshal([]byte(arr), &cs) == nil {
					for _, c := range cs {
						s.Comments = append(s.Comments, normalizeComment(c))
					}
				}
			}
		}
	}

	// If the model ignored the framing entirely, keep everything as the summary
	// so the review is never lost.
	if s.Summary == "" && len(s.Comments) == 0 {
		s.Summary = text
	}
	if s.Recommendation == "" {
		s.Recommendation = "comment"
	}
	return s, nil
}

func normalizeComment(c DraftComment) DraftComment {
	if c.Side == "" {
		c.Side = "RIGHT"
	}
	return c
}

func parseRecommendation(line string) string {
	l := strings.TrimSpace(line)
	if !strings.HasPrefix(strings.ToLower(l), "recommendation:") {
		return ""
	}
	v := strings.ToLower(strings.Trim(strings.TrimSpace(l[len("recommendation:"):]), "`* "))
	switch {
	case strings.HasPrefix(v, "approve"):
		return "approve"
	case strings.HasPrefix(v, "block"):
		return "block"
	case strings.HasPrefix(v, "comment"):
		return "comment"
	}
	return ""
}
