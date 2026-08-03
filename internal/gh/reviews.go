package gh

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ReviewComment is one inline comment in a review payload. Line is the head-side
// line number; Side is RIGHT (new) / LEFT (old). Body is the rendered markdown.
type ReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

// restPost POSTs a JSON payload and returns the response body and status code.
// Mirrors restGet's auth/retry behaviour; the status is returned (not turned
// into an error) so callers can distinguish e.g. a 422 from a transport failure.
func (c *Client) restPost(path string, payload any) ([]byte, int, error) {
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	var lastErr error
	for attempt := 0; attempt < c.maxRetries; attempt++ {
		req, err := http.NewRequest(http.MethodPost, c.restBase+path, bytes.NewReader(reqBody))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "pr-residents-sync")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if attempt < c.maxRetries-1 {
				time.Sleep(backoff(attempt))
				continue
			}
			return nil, 0, &Error{Msg: err.Error()}
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if retryableStatus[resp.StatusCode] && attempt < c.maxRetries-1 {
			lastErr = &Error{Msg: fmt.Sprintf("HTTP %d", resp.StatusCode)}
			time.Sleep(backoff(attempt))
			continue
		}
		return body, resp.StatusCode, nil
	}
	if lastErr != nil {
		return nil, 0, &Error{Msg: lastErr.Error()}
	}
	return nil, 0, &Error{Msg: "restPost: exhausted retries"}
}

// PostPendingReview adds comments to the viewer's existing pending review on the
// PR, or creates a new pending review if none exists. GitHub allows only one
// pending review per user, so a plain create 422s when one already exists;
// appending keeps the review unsubmitted for the user to co-sign either way.
func (c *Client) PostPendingReview(owner, name string, number int, commitID string, comments []ReviewComment) error {
	reviewID, err := c.pendingReviewID(owner, name, number)
	if err != nil {
		return err
	}
	if reviewID == "" {
		return c.CreatePendingReview(owner, name, number, commitID, comments)
	}
	for _, cm := range comments {
		if err := c.addReviewThread(reviewID, cm); err != nil {
			return err
		}
	}
	return nil
}

// CreatePendingReview posts a draft (pending) review: comments are attached but
// no "event" is sent, so GitHub leaves the review unsubmitted for the user to
// submit manually. commitID pins the review to the reviewed head SHA. Fails with
// a 422 if the viewer already has a pending review — callers that must tolerate
// that go through PostPendingReview.
func (c *Client) CreatePendingReview(owner, name string, number int, commitID string, comments []ReviewComment) error {
	payload := map[string]any{
		"commit_id": commitID,
		"comments":  comments,
	}
	body, status, err := c.restPost(fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, name, number), payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return &Error{Msg: fmt.Sprintf("HTTP %d: %s", status, string(body))}
	}
	return nil
}

// pendingReviewID returns the node ID of the viewer's existing PENDING review on
// the PR, or "" if there is none. GitHub returns a PENDING review only to its
// own author, so a match needs no extra login comparison.
func (c *Client) pendingReviewID(owner, name string, number int) (string, error) {
	const q = `query($owner:String!,$name:String!,$number:Int!){
  repository(owner:$owner,name:$name){
    pullRequest(number:$number){
      reviews(first:100, states:[PENDING]){ nodes{ id } }
    }
  }
}`
	data, err := c.graphql(q, map[string]any{"owner": owner, "name": name, "number": number})
	if err != nil {
		return "", err
	}
	var resp struct {
		Repository struct {
			PullRequest struct {
				Reviews struct {
					Nodes []struct {
						ID string `json:"id"`
					} `json:"nodes"`
				} `json:"reviews"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", err
	}
	if n := resp.Repository.PullRequest.Reviews.Nodes; len(n) > 0 {
		return n[0].ID, nil
	}
	return "", nil
}

// addReviewThread appends one inline comment to an existing pending review.
// addPullRequestReviewThread is the only API that adds to a pending review
// without submitting it (the REST reviews endpoint can only create).
func (c *Client) addReviewThread(reviewID string, cm ReviewComment) error {
	const m = `mutation($reviewId:ID!,$path:String!,$line:Int!,$side:DiffSide!,$body:String!){
  addPullRequestReviewThread(input:{pullRequestReviewId:$reviewId,path:$path,line:$line,side:$side,body:$body}){ thread{ id } }
}`
	side := cm.Side
	if side == "" {
		side = "RIGHT"
	}
	_, err := c.graphql(m, map[string]any{
		"reviewId": reviewID, "path": cm.Path, "line": cm.Line, "side": side, "body": cm.Body,
	})
	return err
}
