package gh

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// restClient returns a client whose REST calls hit the given round-trip fn.
func restClient(fn func(*http.Request) (*http.Response, error)) *Client {
	c := NewClient("tok")
	c.restBase = "http://test"
	c.http = &http.Client{Transport: stubRT{fn: fn}}
	return c
}

func TestCreatePendingReview(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	c := restClient(func(r *http.Request) (*http.Response, error) {
		gotMethod, gotPath = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})

	comments := []ReviewComment{{Path: "a.go", Line: 12, Side: "RIGHT", Body: "**issue:** boom"}}
	if err := c.CreatePendingReview("o", "r", 5, "abc", comments); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/repos/o/r/pulls/5/reviews" {
		t.Errorf("method=%s path=%s", gotMethod, gotPath)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("body not json: %v (%s)", err, gotBody)
	}
	if _, ok := payload["event"]; ok {
		t.Error("pending review must not carry an event")
	}
	if payload["commit_id"] != "abc" {
		t.Errorf("commit_id: %v", payload["commit_id"])
	}
	cs, ok := payload["comments"].([]any)
	if !ok || len(cs) != 1 {
		t.Fatalf("comments: %v", payload["comments"])
	}
	c0 := cs[0].(map[string]any)
	if c0["path"] != "a.go" || c0["side"] != "RIGHT" || c0["body"] != "**issue:** boom" {
		t.Errorf("comment: %v", c0)
	}
}

func TestCreatePendingReviewSurfacesError(t *testing.T) {
	c := restClient(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 422,
			Body:       io.NopCloser(strings.NewReader(`{"message":"already has a pending review"}`)),
			Header:     make(http.Header),
		}, nil
	})
	err := c.CreatePendingReview("o", "r", 5, "abc", nil)
	if err == nil || !strings.Contains(err.Error(), "pending review") {
		t.Fatalf("expected GitHub error surfaced, got %v", err)
	}
}
