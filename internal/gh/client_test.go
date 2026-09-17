package gh

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type stubRT struct {
	fn func(*http.Request) (*http.Response, error)
}

func (s stubRT) RoundTrip(r *http.Request) (*http.Response, error) { return s.fn(r) }

func dataResp(data string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"data":` + data + `}`)),
		Header:     make(http.Header),
	}
}

// stubClient dispatches on the query body to canned GraphQL envelopes.
func stubClient(t *testing.T) *Client {
	t.Helper()
	c := NewClient("tok")
	c.endpoint = "http://test/graphql"
	c.http = &http.Client{Transport: stubRT{fn: func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		q := string(body)
		// Use unambiguous markers: the detail query contains "requestedReviewer"
		// (matches a naive "viewer" check) and the count query contains both
		// "search" and "issueCount".
		switch {
		case strings.Contains(q, "viewer { login }"):
			return dataResp(`{"viewer":{"login":"wjones127"}}`), nil
		case strings.Contains(q, "issueCount"):
			return dataResp(`{"search":{"issueCount":7}}`), nil
		case strings.Contains(q, "search(query"):
			return dataResp(`{"search":{
				"pageInfo":{"hasNextPage":false,"endCursor":null},
				"nodes":[
					{"number":7416,"updatedAt":"2026-06-23T00:00:00Z","headRefOid":"abc","repository":{"nameWithOwner":"lancedb/lance"}},
					{}
				]}}`), nil
		case strings.Contains(q, "pullRequest(number"):
			return dataResp(`{"repository":{"pullRequest":{
				"number":7416,"title":"t","url":"u","body":"b","isDraft":false,
				"author":{"login":"alice"},"headRefOid":"abc","mergeable":"MERGEABLE",
				"additions":20,"deletions":5,"changedFiles":2,
				"labels":{"nodes":[{"name":"bug"}]},
				"files":{"nodes":[{"path":"rust/x.rs","additions":10,"deletions":2}]},
				"reviews":{"nodes":[{"author":{"login":"wjones127"},"state":"COMMENTED","submittedAt":"2026-06-22T00:00:00Z","commit":{"oid":"old"}}]},
				"commits":{"nodes":[{"commit":{"oid":"abc","committedDate":"2026-06-23T00:00:00Z","statusCheckRollup":{"state":"SUCCESS"}}}]},
				"timelineItems":{"nodes":[]}
			}}}`), nil
		}
		t.Fatalf("unexpected query: %s", q)
		return nil, nil
	}}}
	return c
}

func TestViewerLogin(t *testing.T) {
	got, err := stubClient(t).ViewerLogin()
	if err != nil || got != "wjones127" {
		t.Fatalf("viewer=%q err=%v", got, err)
	}
}

func TestSearchLight(t *testing.T) {
	hits, err := stubClient(t).SearchLight("repo:lancedb/lance is:open is:pr")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 { // the empty node is skipped
		t.Fatalf("expected 1 hit, got %d: %+v", len(hits), hits)
	}
	h := hits[0]
	if h.Repo != "lancedb/lance" || h.Number != 7416 || h.HeadRefOid != "abc" || h.UpdatedAt != "2026-06-23T00:00:00Z" {
		t.Errorf("hit: %+v", h)
	}
}

func TestSearchCount(t *testing.T) {
	n, err := stubClient(t).SearchCount("repo:x is:pr is:merged author:a")
	if err != nil || n != 7 {
		t.Fatalf("count=%d err=%v", n, err)
	}
}

func TestFetchDetail(t *testing.T) {
	d, warns, err := stubClient(t).FetchDetail("lancedb", "lance", 7416)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if d.Number != 7416 || d.Author.Login != "alice" || d.Mergeable != "MERGEABLE" {
		t.Errorf("detail scalars: %+v", d)
	}
	if len(d.Files.Nodes) != 1 || d.Files.Nodes[0].Path != "rust/x.rs" {
		t.Errorf("files: %+v", d.Files)
	}
	if len(d.Reviews.Nodes) != 1 || d.Reviews.Nodes[0].Commit.Oid != "old" {
		t.Errorf("reviews: %+v", d.Reviews)
	}
	if len(d.Commits.Nodes) != 1 || d.Commits.Nodes[0].Commit.StatusCheckRollup.State != "SUCCESS" {
		t.Errorf("commits: %+v", d.Commits)
	}
	if len(d.Labels.Nodes) != 1 || d.Labels.Nodes[0].Name != "bug" {
		t.Errorf("labels: %+v", d.Labels)
	}
}

func TestGraphQLErrors(t *testing.T) {
	c := NewClient("tok")
	c.endpoint = "http://test/graphql"
	c.http = &http.Client{Transport: stubRT{fn: func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"errors":[{"message":"boom"}]}`)),
			Header:     make(http.Header),
		}, nil
	}}}
	if _, err := c.ViewerLogin(); err == nil {
		t.Error("expected error from GraphQL errors payload")
	}
}

// fixedBodyClient returns a client whose every GraphQL POST yields body.
func fixedBodyClient(body string) *Client {
	c := NewClient("tok")
	c.endpoint = "http://test/graphql"
	c.http = &http.Client{Transport: stubRT{fn: func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	}}}
	return c
}

// A fine-grained PAT can't read Checks, so statusCheckRollup contexts come back
// FORBIDDEN alongside otherwise-complete data. FetchDetail should keep the PR and
// warn, not fail.
func TestFetchDetailToleratesChecksForbidden(t *testing.T) {
	body := `{"data":{"repository":{"pullRequest":{
		"number":6681,"title":"t","url":"u","author":{"login":"alice"},
		"headRefOid":"abc","mergeable":"MERGEABLE",
		"commits":{"nodes":[{"commit":{"oid":"abc","committedDate":"2026-06-23T00:00:00Z","statusCheckRollup":null}}]},
		"timelineItems":{"nodes":[]}
	}}},"errors":[
		{"type":"FORBIDDEN","path":["repository","pullRequest","commits","nodes",0,"commit","statusCheckRollup","contexts","nodes",0],"message":"Resource not accessible by personal access token"},
		{"type":"FORBIDDEN","path":["repository","pullRequest","commits","nodes",0,"commit","statusCheckRollup","contexts","nodes",1],"message":"Resource not accessible by personal access token"}
	]}`
	d, warns, err := fixedBodyClient(body).FetchDetail("lancedb", "sophon", 6681)
	if err != nil {
		t.Fatalf("expected tolerated partial, got error: %v", err)
	}
	if d == nil || d.Number != 6681 {
		t.Fatalf("expected detail for 6681, got %+v", d)
	}
	if len(warns) != 1 {
		t.Fatalf("expected 1 warning, got %v", warns)
	}
}

// An error that isn't confined to statusCheckRollup must still fail the fetch.
func TestFetchDetailFailsOnNonCheckError(t *testing.T) {
	body := `{"data":{"repository":{"pullRequest":null}},"errors":[
		{"type":"FORBIDDEN","path":["repository","pullRequest"],"message":"Resource not accessible by personal access token"}
	]}`
	if _, _, err := fixedBodyClient(body).FetchDetail("lancedb", "sophon", 6681); err == nil {
		t.Error("expected error when a non-statusCheckRollup field is forbidden")
	}
}

func TestPullFilesBackfillsOmittedPatch(t *testing.T) {
	var diffCalls int
	c := restClient(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Accept") == "application/vnd.github.v3.diff" {
			diffCalls++
			return textResp("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n@@ -1 +1 @@\n-x\n+y\n")
		}
		return jsonResp(`[{"filename":"small.go","status":"modified","additions":1,"deletions":1,"patch":"@@ -1 +1 @@\n-a\n+b"},
		                  {"filename":"big.go","status":"modified","additions":900,"deletions":900},
		                  {"filename":"img.png","status":"added","additions":0,"deletions":0}]`)
	})

	files, err := c.PullFiles("o", "r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if diffCalls != 1 {
		t.Errorf("raw diff fetched %d times, want 1", diffCalls)
	}
	byName := map[string]FileDiff{}
	for _, f := range files {
		byName[f.Filename] = f
	}
	if want := "@@ -1 +1 @@\n-x\n+y"; byName["big.go"].Patch != want {
		t.Errorf("big.go patch not backfilled: %q", byName["big.go"].Patch)
	}
	if want := "@@ -1 +1 @@\n-a\n+b"; byName["small.go"].Patch != want {
		t.Errorf("small.go patch clobbered: %q", byName["small.go"].Patch)
	}
	if byName["img.png"].Patch != "" {
		t.Errorf("binary file should stay patch-less: %q", byName["img.png"].Patch)
	}
}

func TestPullFilesSkipsDiffFetchWhenComplete(t *testing.T) {
	var diffCalls int
	c := restClient(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Accept") == "application/vnd.github.v3.diff" {
			diffCalls++
			return textResp("")
		}
		return jsonResp(`[{"filename":"a.go","status":"modified","patch":"@@ -1 +1 @@\n-a\n+b"}]`)
	})
	if _, err := c.PullFiles("o", "r", 7); err != nil {
		t.Fatal(err)
	}
	if diffCalls != 0 {
		t.Errorf("no patch is missing; raw diff should not be fetched (%d calls)", diffCalls)
	}
}

func TestPullFilesSurvivesDiffFetchFailure(t *testing.T) {
	c := restClient(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Accept") == "application/vnd.github.v3.diff" {
			return &http.Response{StatusCode: 406, Body: io.NopCloser(strings.NewReader("too big")), Header: make(http.Header)}, nil
		}
		return jsonResp(`[{"filename":"big.go","status":"modified"}]`)
	})
	files, err := c.PullFiles("o", "r", 7)
	if err != nil {
		t.Fatalf("a failed backfill must not fail the fetch: %v", err)
	}
	if len(files) != 1 || files[0].Patch != "" {
		t.Errorf("files: %+v", files)
	}
}

// textResp is a 200 plain-text response for the stub transport.
func textResp(body string) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}
