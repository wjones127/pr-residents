package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lancedb/pr-residents/internal/config"
	"github.com/lancedb/pr-residents/internal/prr"
)

func TestClaudeWorkupParsesEnvelope(t *testing.T) {
	var gotArgs []string
	var gotStdin string
	result := "RECOMMENDATION: block\n===SUMMARY===\nLooks risky.\n===COMMENTS===\n" +
		`{"path":"a.go","line":12,"label":"issue","blocking":true,"body":"guard nil"}` + "\n" +
		`{"label":"praise","body":"nice test"}`
	ag := &ClaudeAgent{Bin: "claude", Run: func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		gotArgs = args
		gotStdin = stdin
		env := map[string]any{"result": result, "usage": map[string]any{"input_tokens": 120, "output_tokens": 45}}
		b, _ := json.Marshal(env)
		return b, nil
	}}

	soap, err := ag.Workup(context.Background(), "PROMPT-BODY", "sonnet")
	if err != nil {
		t.Fatal(err)
	}
	if soap.Recommendation != "block" || soap.Summary != "Looks risky." {
		t.Errorf("soap header/summary: %+v", soap)
	}
	if len(soap.Comments) != 2 || soap.Comments[0].Path != "a.go" || soap.Comments[0].Line != 12 || !soap.Comments[0].Blocking {
		t.Errorf("comments: %+v", soap.Comments)
	}
	if soap.Comments[0].Side != "RIGHT" { // defaulted
		t.Errorf("side should default to RIGHT: %+v", soap.Comments[0])
	}
	if soap.BlockingCount() != 1 {
		t.Errorf("blocking count: %d", soap.BlockingCount())
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
		return []byte(`{"result":"RECOMMENDATION: comment\n===SUMMARY===\nok","usage":{}}`), nil
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

func TestParseSOAPSkipsBadCommentLine(t *testing.T) {
	// A malformed comment line must not discard the good ones.
	raw := "RECOMMENDATION: approve\n===SUMMARY===\nAll good.\n===COMMENTS===\n" +
		"{ this is not json\n" +
		`{"path":"b.go","line":3,"label":"suggestion","body":"rename"}`
	s, err := parseSOAP(raw)
	if err != nil {
		t.Fatal(err)
	}
	if s.Recommendation != "approve" || s.Summary != "All good." {
		t.Errorf("header/summary: %+v", s)
	}
	if len(s.Comments) != 1 || s.Comments[0].Path != "b.go" {
		t.Errorf("should keep the one valid comment: %+v", s.Comments)
	}
}

func TestParseSOAPFallsBackToSummary(t *testing.T) {
	// Model ignored the framing entirely — keep the whole thing as the summary.
	s, err := parseSOAP("just some review prose with no framing")
	if err != nil {
		t.Fatal(err)
	}
	if s.Summary != "just some review prose with no framing" || s.Recommendation != "comment" {
		t.Errorf("fallback: %+v", s)
	}
}

func TestParseSOAPRejectsEmpty(t *testing.T) {
	if _, err := parseSOAP("   "); err == nil {
		t.Error("empty output should error")
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
