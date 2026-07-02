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

// parseSOAP extracts the JSON SOAP contract from the model's result text,
// tolerating a stray code fence or surrounding prose by taking the outermost
// {...}. (One lenient parse rather than a costly re-invoke.)
func parseSOAP(result string) (SOAP, error) {
	raw := strings.TrimSpace(result)
	if start := strings.IndexByte(raw, '{'); start >= 0 {
		if end := strings.LastIndexByte(raw, '}'); end > start {
			raw = raw[start : end+1]
		}
	}
	var s SOAP
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return SOAP{}, fmt.Errorf("parse SOAP JSON: %w", err)
	}
	if s.Text == "" {
		return SOAP{}, fmt.Errorf("SOAP had empty soap text")
	}
	return s, nil
}
