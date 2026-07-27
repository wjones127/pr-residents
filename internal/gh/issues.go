package gh

import "encoding/json"

// Linked-issue pass: the issues a PR closes (via "Fixes #N" keywords or the
// Development sidebar) plus their bodies and discussion. This is the premise
// context a reviewer needs to judge whether the change solves a real problem —
// the issue states the problem, and its comments usually carry the refining
// discussion that narrowed the solution.
const linkedIssuesQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      closingIssuesReferences(first: 5) {
        nodes {
          number title state body
          comments(first: 50) {
            nodes { author { login } body }
          }
        }
      }
    }
  }
}`

// IssueComment is one comment on a linked issue, reduced to author + body.
type IssueComment struct {
	Author string
	Body   string
}

// LinkedIssue is one issue the PR closes, with its discussion.
type LinkedIssue struct {
	Number   int
	Title    string
	State    string
	Body     string
	Comments []IssueComment
}

// FetchLinkedIssues returns the issues this PR closes, each with its body and
// comments. A PR with no closing references yields an empty slice, not an error.
func (c *Client) FetchLinkedIssues(owner, name string, number int) ([]LinkedIssue, error) {
	data, err := c.graphql(linkedIssuesQuery, map[string]any{"owner": owner, "name": name, "number": number})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Repository struct {
			PullRequest *struct {
				ClosingIssuesReferences struct {
					Nodes []struct {
						Number   int    `json:"number"`
						Title    string `json:"title"`
						State    string `json:"state"`
						Body     string `json:"body"`
						Comments struct {
							Nodes []struct {
								Author *Actor `json:"author"`
								Body   string `json:"body"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"closingIssuesReferences"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	pr := resp.Repository.PullRequest
	if pr == nil {
		return nil, nil
	}
	var out []LinkedIssue
	for _, n := range pr.ClosingIssuesReferences.Nodes {
		li := LinkedIssue{Number: n.Number, Title: n.Title, State: n.State, Body: n.Body}
		for _, cm := range n.Comments.Nodes {
			ic := IssueComment{Body: cm.Body}
			if cm.Author != nil {
				ic.Author = cm.Author.Login
			}
			li.Comments = append(li.Comments, ic)
		}
		out = append(out, li)
	}
	return out, nil
}
