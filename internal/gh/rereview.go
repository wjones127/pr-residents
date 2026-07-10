package gh

import (
	"encoding/json"
	"fmt"
)

// Re-review pass: reviews + review threads (with the root comment) for
// reconstructing the conditions ledger, plus head/CI/mergeability.
const reReviewQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number title url headRefOid mergeable
      commits(last: 1) { nodes { commit { statusCheckRollup {
        state
        contexts(first: 100) { nodes {
          __typename
          ... on CheckRun { name conclusion }
          ... on StatusContext { context state }
        } }
      } } } }
      reviews(last: 50) {
        nodes { author { login } state submittedAt commit { oid } }
      }
      reviewThreads(first: 100) {
        nodes {
          id isResolved isOutdated path line originalLine
          comments(first: 1) { nodes { author { login } body } }
        }
      }
    }
  }
}`

// ReviewThread is one review thread reduced to its root comment + location.
type ReviewThread struct {
	ID           string
	IsResolved   bool
	IsOutdated   bool
	Path         string
	Line         int
	OriginalLine int
	RootAuthor   string
	RootBody     string
}

// ReReviewPR is the data a re-review packet is built from.
type ReReviewPR struct {
	Number         int
	Title          string
	URL            string
	Head           string
	Mergeable      string
	RollupState    string
	RollupContexts []RollupContext
	Reviews        []Review
	Threads        []ReviewThread
}

// FetchReReviewData fetches reviews + review threads + head/CI for one PR.
func (c *Client) FetchReReviewData(owner, name string, number int) (ReReviewPR, error) {
	data, err := c.graphql(reReviewQuery, map[string]any{"owner": owner, "name": name, "number": number})
	if err != nil {
		return ReReviewPR{}, err
	}
	var resp struct {
		Repository struct {
			PullRequest *struct {
				Number        int     `json:"number"`
				Title         string  `json:"title"`
				URL           string  `json:"url"`
				HeadRefOid    string  `json:"headRefOid"`
				Mergeable     string  `json:"mergeable"`
				Commits       Commits `json:"commits"`
				Reviews       Reviews `json:"reviews"`
				ReviewThreads struct {
					Nodes []struct {
						ID           string `json:"id"`
						IsResolved   bool   `json:"isResolved"`
						IsOutdated   bool   `json:"isOutdated"`
						Path         string `json:"path"`
						Line         int    `json:"line"`
						OriginalLine int    `json:"originalLine"`
						Comments     struct {
							Nodes []struct {
								Author *Actor `json:"author"`
								Body   string `json:"body"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return ReReviewPR{}, err
	}
	pr := resp.Repository.PullRequest
	if pr == nil {
		return ReReviewPR{}, &Error{Msg: fmt.Sprintf("no pull request %s/%s#%d", owner, name, number)}
	}

	out := ReReviewPR{
		Number: pr.Number, Title: pr.Title, URL: pr.URL, Head: pr.HeadRefOid,
		Mergeable: pr.Mergeable, Reviews: pr.Reviews.Nodes,
	}
	if len(pr.Commits.Nodes) > 0 && pr.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		rollup := pr.Commits.Nodes[0].Commit.StatusCheckRollup
		out.RollupState = rollup.State
		out.RollupContexts = rollup.Contexts.Nodes
	}
	for _, t := range pr.ReviewThreads.Nodes {
		th := ReviewThread{
			ID: t.ID, IsResolved: t.IsResolved, IsOutdated: t.IsOutdated,
			Path: t.Path, Line: t.Line, OriginalLine: t.OriginalLine,
		}
		if len(t.Comments.Nodes) > 0 {
			th.RootBody = t.Comments.Nodes[0].Body
			if t.Comments.Nodes[0].Author != nil {
				th.RootAuthor = t.Comments.Nodes[0].Author.Login
			}
		}
		out.Threads = append(out.Threads, th)
	}
	return out, nil
}

// CompareResult is a REST compare(base...head): its status ("ahead" when base is
// an ancestor of head) plus the commits and files in the range.
type CompareResult struct {
	Status  string `json:"status"`
	Commits []struct {
		SHA string `json:"sha"`
	} `json:"commits"`
	Files []FileDiff `json:"files"`
}

// Compare returns the commit-anchored diff base...head (trap #2: anchor on the
// sha last reviewed, not GitHub's built-in "changes since").
func (c *Client) Compare(owner, name, base, head string) (CompareResult, error) {
	body, err := c.restGet(fmt.Sprintf("/repos/%s/%s/compare/%s...%s", owner, name, base, head))
	if err != nil {
		return CompareResult{}, err
	}
	var out CompareResult
	if err := json.Unmarshal(body, &out); err != nil {
		return CompareResult{}, err
	}
	return out, nil
}
