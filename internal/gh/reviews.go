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

// CreatePendingReview posts a draft (pending) review: comments are attached but
// no "event" is sent, so GitHub leaves the review unsubmitted for the user to
// submit manually. commitID pins the review to the reviewed head SHA.
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
