package agent

import (
	"context"
	"testing"

	"github.com/lancedb/pr-residents/internal/config"
	"github.com/lancedb/pr-residents/internal/prr"
)

func TestClaudeWorkupParsesEnvelope(t *testing.T) {
	var gotArgs []string
	var gotStdin string
	ag := &ClaudeAgent{Bin: "claude", Run: func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		gotArgs = args
		gotStdin = stdin
		return []byte(`{"result":"{\"soap\":\"REVIEW ok\",\"recommendation\":\"approve\",\"blocking_count\":0}","usage":{"input_tokens":120,"output_tokens":45}}`), nil
	}}

	soap, err := ag.Workup(context.Background(), "PROMPT-BODY", "sonnet")
	if err != nil {
		t.Fatal(err)
	}
	if soap.Text != "REVIEW ok" || soap.Recommendation != "approve" {
		t.Errorf("soap: %+v", soap)
	}
	if soap.TokensIn != 120 || soap.TokensOut != 45 {
		t.Errorf("tokens: %d/%d", soap.TokensIn, soap.TokensOut)
	}
	if !contains(gotArgs, "--model") || !contains(gotArgs, "sonnet") || !contains(gotArgs, "-p") {
		t.Errorf("args: %+v", gotArgs)
	}
	if gotStdin != "PROMPT-BODY" {
		t.Errorf("prompt should be passed verbatim on stdin, got %q", gotStdin)
	}
}

func TestClaudeWorkupOmitsModelWhenEmpty(t *testing.T) {
	var gotArgs []string
	ag := &ClaudeAgent{Bin: "claude", Run: func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"result":"{\"soap\":\"x\",\"recommendation\":\"comment\"}","usage":{}}`), nil
	}}
	if _, err := ag.Workup(context.Background(), "PROMPT", ""); err != nil {
		t.Fatal(err)
	}
	if contains(gotArgs, "--model") {
		t.Errorf("empty model should omit --model: %+v", gotArgs)
	}
}

func TestClaudeWorkupErrorEnvelope(t *testing.T) {
	ag := &ClaudeAgent{Bin: "claude", Run: func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		return []byte(`{"result":"boom","is_error":true}`), nil
	}}
	if _, err := ag.Workup(context.Background(), "PROMPT", ""); err == nil {
		t.Error("expected error from is_error envelope")
	}
}

func TestParseSOAPLenient(t *testing.T) {
	// Surrounding prose + a code fence around the JSON.
	raw := "Here you go:\n```json\n{\"soap\":\"REVIEW\",\"recommendation\":\"block\",\"blocking_count\":2}\n```\n"
	s, err := parseSOAP(raw)
	if err != nil {
		t.Fatal(err)
	}
	if s.Recommendation != "block" || s.BlockingCount != 2 || s.Text != "REVIEW" {
		t.Errorf("soap: %+v", s)
	}
}

func TestParseSOAPRejectsEmpty(t *testing.T) {
	if _, err := parseSOAP(`{"soap":"","recommendation":"approve"}`); err == nil {
		t.Error("empty soap text should be rejected")
	}
}

func TestModelFor(t *testing.T) {
	d := config.Dispatch{ModelRouting: map[string]string{
		"fresh_xl": "opus", "re_review": "sonnet", "xs": "haiku", "default": "sonnet",
	}}
	cases := []struct {
		lane, size string
		want       string
	}{
		{"fresh", "XL", "opus"},      // lane_size most specific
		{"re_review", "M", "sonnet"}, // lane
		{"fresh", "XS", "haiku"},     // size
		{"fresh", "M", "sonnet"},     // default
	}
	for _, c := range cases {
		r := &prr.Record{Lane: c.lane, Effort: prr.Effort{SizeBucket: c.size}}
		if got := ModelFor(d, r); got != c.want {
			t.Errorf("ModelFor(%s,%s)=%q want %q", c.lane, c.size, got, c.want)
		}
	}
	if got := ModelFor(config.Dispatch{}, &prr.Record{Lane: "fresh", Effort: prr.Effort{SizeBucket: "M"}}); got != "" {
		t.Errorf("empty routing should return empty, got %q", got)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
