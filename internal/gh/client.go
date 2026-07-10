package gh

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultEndpoint = "https://api.github.com/graphql"

// Lightweight pass: just enough to decide which PRs changed since last sync.
const searchQuery = `
query($q: String!, $cursor: String) {
  search(query: $q, type: ISSUE, first: 50, after: $cursor) {
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest {
        number
        updatedAt
        headRefOid
        repository { nameWithOwner }
      }
    }
  }
}`

// Count-only pass: how many PRs a query matches (e.g. an author's merged PRs).
const searchCountQuery = `
query($q: String!) {
  search(query: $q, type: ISSUE, first: 0) { issueCount }
}`

// Relevance pass: PRs carrying their changed paths + reviews, for scoring
// triage candidates and building the review-history profile.
const searchFilesQuery = `
query($q: String!, $cursor: String) {
  search(query: $q, type: ISSUE, first: 25, after: $cursor) {
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest {
        number
        title
        url
        author { login }
        repository { nameWithOwner }
        files(first: 100) { nodes { path } }
        reviews(first: 20) { nodes { author { login __typename } state } }
      }
    }
  }
}`

// Heavy pass: full detail for a single PR, fetched only on cache miss / change.
const detailQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number
      title
      url
      body
      isDraft
      createdAt
      updatedAt
      author { login }
      headRefOid
      reviewDecision
      mergeable
      additions
      deletions
      changedFiles
      labels(first: 30) { nodes { name } }
      files(first: 100) { nodes { path additions deletions } }
      reviewRequests(first: 30) {
        nodes {
          requestedReviewer {
            __typename
            ... on User { login }
            ... on Team { slug }
          }
        }
      }
      reviews(last: 50) {
        nodes { author { login } state submittedAt commit { oid } }
      }
      commits(last: 1) {
        nodes { commit { oid committedDate statusCheckRollup {
          state
          contexts(first: 100) { nodes {
            __typename
            ... on CheckRun { name conclusion }
            ... on StatusContext { context state }
          } }
        } } }
      }
      timelineItems(last: 15, itemTypes: [HEAD_REF_FORCE_PUSHED_EVENT, PULL_REQUEST_COMMIT]) {
        nodes {
          __typename
          ... on HeadRefForcePushedEvent { createdAt }
          ... on PullRequestCommit { commit { committedDate } }
        }
      }
    }
  }
}`

// Error is a GitHub API error.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// LightPR is one row from the lightweight search pass.
type LightPR struct {
	Repo       string
	Number     int
	UpdatedAt  string
	HeadRefOid string
}

const defaultRESTBase = "https://api.github.com"

// Client is a GitHub GraphQL client. Auth is a per-org token passed explicitly
// (no gh keyring, no env magic) so behaviour is identical everywhere.
type Client struct {
	token      string
	http       *http.Client
	maxRetries int
	endpoint   string
	restBase   string
}

// NewClient returns a client authenticated with token.
func NewClient(token string) *Client {
	return &Client{
		token:      token,
		http:       &http.Client{Timeout: 30 * time.Second},
		maxRetries: 3,
		endpoint:   defaultEndpoint,
		restBase:   defaultRESTBase,
	}
}

// FileDiff is one changed file in a PR (REST pulls/{n}/files). Patch is empty
// for binary/too-large files.
type FileDiff struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch"`
}

func (c *Client) restGet(path string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < c.maxRetries; attempt++ {
		req, err := http.NewRequest(http.MethodGet, c.restBase+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "pr-residents-sync")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if attempt < c.maxRetries-1 {
				time.Sleep(backoff(attempt))
				continue
			}
			return nil, &Error{Msg: err.Error()}
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			if retryableStatus[resp.StatusCode] && attempt < c.maxRetries-1 {
				lastErr = &Error{Msg: fmt.Sprintf("HTTP %d", resp.StatusCode)}
				time.Sleep(backoff(attempt))
				continue
			}
			return nil, &Error{Msg: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body))}
		}
		return body, nil
	}
	if lastErr != nil {
		return nil, &Error{Msg: lastErr.Error()}
	}
	return nil, &Error{Msg: "restGet: exhausted retries"}
}

// PullFiles returns a PR's net changed files with patches (the 3-dot diff vs the
// merge base), following pagination. GitHub omits Patch for binary/large files.
func (c *Client) PullFiles(owner, name string, number int) ([]FileDiff, error) {
	var files []FileDiff
	for page := 1; ; page++ {
		body, err := c.restGet(fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=100&page=%d", owner, name, number, page))
		if err != nil {
			return nil, err
		}
		var batch []FileDiff
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, err
		}
		files = append(files, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return files, nil
}

var retryableStatus = map[int]bool{403: true, 429: true, 502: true, 503: true}

// graphql POSTs a query and returns the raw "data" field.
func (c *Client) graphql(query string, variables map[string]any) (json.RawMessage, error) {
	reqBody, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt < c.maxRetries; attempt++ {
		req, err := http.NewRequest(http.MethodPost, c.endpoint, bytes.NewReader(reqBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "pr-residents-sync")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if attempt < c.maxRetries-1 {
				time.Sleep(backoff(attempt))
				continue
			}
			return nil, &Error{Msg: err.Error()}
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			if retryableStatus[resp.StatusCode] && attempt < c.maxRetries-1 {
				lastErr = &Error{Msg: fmt.Sprintf("HTTP %d", resp.StatusCode)}
				time.Sleep(backoff(attempt))
				continue
			}
			return nil, &Error{Msg: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body))}
		}

		var payload struct {
			Data   json.RawMessage `json:"data"`
			Errors json.RawMessage `json:"errors"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, &Error{Msg: err.Error()}
		}
		if len(payload.Errors) > 0 && string(payload.Errors) != "null" {
			return nil, &Error{Msg: string(payload.Errors)}
		}
		return payload.Data, nil
	}
	if lastErr != nil {
		return nil, &Error{Msg: lastErr.Error()}
	}
	return nil, &Error{Msg: "graphql: exhausted retries"}
}

func backoff(attempt int) time.Duration {
	return time.Duration(1<<attempt) * time.Second
}

// ViewerLogin returns the token owner's login.
func (c *Client) ViewerLogin() (string, error) {
	data, err := c.graphql("query { viewer { login } }", map[string]any{})
	if err != nil {
		return "", err
	}
	var out struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	return out.Viewer.Login, nil
}

// SearchLight returns [{repo, number, updatedAt, headRefOid}] for a search query,
// following pagination.
func (c *Client) SearchLight(query string) ([]LightPR, error) {
	var out []LightPR
	var cursor any
	for {
		data, err := c.graphql(searchQuery, map[string]any{"q": query, "cursor": cursor})
		if err != nil {
			return nil, err
		}
		var resp struct {
			Search struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					Number     int    `json:"number"`
					UpdatedAt  string `json:"updatedAt"`
					HeadRefOid string `json:"headRefOid"`
					Repository struct {
						NameWithOwner string `json:"nameWithOwner"`
					} `json:"repository"`
				} `json:"nodes"`
			} `json:"search"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, err
		}
		for _, n := range resp.Search.Nodes {
			if n.Repository.NameWithOwner == "" {
				continue // empty node (a non-PR search hit)
			}
			out = append(out, LightPR{
				Repo:       n.Repository.NameWithOwner,
				Number:     n.Number,
				UpdatedAt:  n.UpdatedAt,
				HeadRefOid: n.HeadRefOid,
			})
		}
		if !resp.Search.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Search.PageInfo.EndCursor
	}
	return out, nil
}

// SearchCount returns the total matches for a search query.
func (c *Client) SearchCount(query string) (int, error) {
	data, err := c.graphql(searchCountQuery, map[string]any{"q": query})
	if err != nil {
		return 0, err
	}
	var resp struct {
		Search struct {
			IssueCount int `json:"issueCount"`
		} `json:"search"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, err
	}
	return resp.Search.IssueCount, nil
}

// CandidateReview is a review on a relevance candidate (login + type + state).
type CandidateReview struct {
	Login string
	Type  string
	State string
}

// CandidatePR is a relevance candidate: identity, changed paths, and reviews.
type CandidatePR struct {
	Number  int
	Title   string
	URL     string
	Author  string
	Repo    string
	Paths   []string
	Reviews []CandidateReview
}

// SearchWithFiles returns candidate PRs (with changed paths + reviews) for a
// search query, up to limit, following pagination.
func (c *Client) SearchWithFiles(query string, limit int) ([]CandidatePR, error) {
	var out []CandidatePR
	var cursor any
	for len(out) < limit {
		data, err := c.graphql(searchFilesQuery, map[string]any{"q": query, "cursor": cursor})
		if err != nil {
			return nil, err
		}
		var resp struct {
			Search struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					Number     int    `json:"number"`
					Title      string `json:"title"`
					URL        string `json:"url"`
					Author     *Actor `json:"author"`
					Repository struct {
						NameWithOwner string `json:"nameWithOwner"`
					} `json:"repository"`
					Files struct {
						Nodes []struct {
							Path string `json:"path"`
						} `json:"nodes"`
					} `json:"files"`
					Reviews struct {
						Nodes []struct {
							Author *struct {
								Login    string `json:"login"`
								Typename string `json:"__typename"`
							} `json:"author"`
							State string `json:"state"`
						} `json:"nodes"`
					} `json:"reviews"`
				} `json:"nodes"`
			} `json:"search"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, err
		}
		for _, n := range resp.Search.Nodes {
			if n.Repository.NameWithOwner == "" {
				continue
			}
			cp := CandidatePR{
				Number: n.Number, Title: n.Title, URL: n.URL,
				Repo: n.Repository.NameWithOwner,
			}
			if n.Author != nil {
				cp.Author = n.Author.Login
			}
			for _, f := range n.Files.Nodes {
				cp.Paths = append(cp.Paths, f.Path)
			}
			for _, rv := range n.Reviews.Nodes {
				cr := CandidateReview{State: rv.State}
				if rv.Author != nil {
					cr.Login = rv.Author.Login
					cr.Type = rv.Author.Typename
				}
				cp.Reviews = append(cp.Reviews, cr)
			}
			out = append(out, cp)
			if len(out) >= limit {
				break
			}
		}
		if !resp.Search.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Search.PageInfo.EndCursor
	}
	return out, nil
}

// FetchDetail fetches the full detail for one PR.
func (c *Client) FetchDetail(owner, name string, number int) (*Detail, error) {
	data, err := c.graphql(detailQuery, map[string]any{"owner": owner, "name": name, "number": number})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Repository struct {
			PullRequest *Detail `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	if resp.Repository.PullRequest == nil {
		return nil, &Error{Msg: fmt.Sprintf("no pull request %s/%s#%d", owner, name, number)}
	}
	return resp.Repository.PullRequest, nil
}
