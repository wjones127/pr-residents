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

// jsonResp is a 200 JSON response for the stub transport.
func jsonResp(body string) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

// When no pending review exists, PostPendingReview falls back to a single REST
// create carrying all comments.
func TestPostPendingReviewCreatesWhenNone(t *testing.T) {
	var restCreated bool
	var mutations int
	c := restClient(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		if strings.Contains(r.URL.Host, "api.github.com") { // GraphQL
			if strings.Contains(body, "addPullRequestReviewThread") {
				mutations++
			}
			return jsonResp(`{"data":{"repository":{"pullRequest":{"reviews":{"nodes":[]}}}}}`)
		}
		// REST reviews create.
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/reviews") {
			restCreated = true
		}
		return jsonResp(`{}`)
	})

	comments := []ReviewComment{{Path: "a.go", Line: 12, Side: "RIGHT", Body: "x"}}
	if err := c.PostPendingReview("o", "r", 5, "abc", comments); err != nil {
		t.Fatal(err)
	}
	if !restCreated {
		t.Error("expected a REST reviews create when no pending review exists")
	}
	if mutations != 0 {
		t.Errorf("expected no thread mutations, got %d", mutations)
	}
}

// When a pending review exists, PostPendingReview appends each comment via a
// GraphQL thread mutation and never hits the REST create (which would 422).
func TestPostPendingReviewAppendsWhenExisting(t *testing.T) {
	var restCreated bool
	var mutations int
	c := restClient(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		if strings.Contains(r.URL.Host, "api.github.com") { // GraphQL
			if strings.Contains(body, "addPullRequestReviewThread") {
				mutations++
				return jsonResp(`{"data":{"addPullRequestReviewThread":{"thread":{"id":"T"}}}}`)
			}
			return jsonResp(`{"data":{"repository":{"pullRequest":{"reviews":{"nodes":[{"id":"PRR_1"}]}}}}}`)
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/reviews") {
			restCreated = true
		}
		return jsonResp(`{}`)
	})

	comments := []ReviewComment{
		{Path: "a.go", Line: 12, Side: "RIGHT", Body: "x"},
		{Path: "b.go", Line: 3, Side: "RIGHT", Body: "y"},
	}
	if err := c.PostPendingReview("o", "r", 5, "abc", comments); err != nil {
		t.Fatal(err)
	}
	if restCreated {
		t.Error("must not create a second review when one is pending")
	}
	if mutations != 2 {
		t.Errorf("expected 2 thread mutations (one per comment), got %d", mutations)
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
