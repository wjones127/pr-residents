// Package gh holds the GitHub GraphQL response types and (later) the client.
// The detail types mirror the pr-sync detail query shape exactly, so a raw
// GraphQL payload unmarshals straight into Detail and the derive package reads
// it without touching maps.
package gh

// Actor is a GitHub user/team reference reduced to its login.
type Actor struct {
	Login string `json:"login"`
}

type Label struct {
	Name string `json:"name"`
}

type Labels struct {
	Nodes []Label `json:"nodes"`
}

type FileNode struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

type Files struct {
	Nodes []FileNode `json:"nodes"`
}

type Review struct {
	Author      *Actor  `json:"author"`
	State       string  `json:"state"`
	SubmittedAt string  `json:"submittedAt"`
	Commit      *Commit `json:"commit"`
}

type Reviews struct {
	Nodes []Review `json:"nodes"`
}

type Rollup struct {
	State string `json:"state"`
}

type Commit struct {
	Oid               string  `json:"oid"`
	CommittedDate     string  `json:"committedDate"`
	StatusCheckRollup *Rollup `json:"statusCheckRollup"`
}

type CommitNode struct {
	Commit Commit `json:"commit"`
}

type Commits struct {
	Nodes []CommitNode `json:"nodes"`
}

// TimelineNode covers the item types pr-sync requests: HeadRefForcePushedEvent
// (createdAt) and PullRequestCommit (commit.committedDate).
type TimelineNode struct {
	Typename  string  `json:"__typename"`
	CreatedAt string  `json:"createdAt"`
	Commit    *Commit `json:"commit"`
}

type Timeline struct {
	Nodes []TimelineNode `json:"nodes"`
}

type ReviewRequests struct {
	Nodes []struct {
		RequestedReviewer *Actor `json:"requestedReviewer"`
	} `json:"nodes"`
}

// Detail is one PR's full detail (the heavy GraphQL pass).
type Detail struct {
	Number         int            `json:"number"`
	Title          string         `json:"title"`
	URL            string         `json:"url"`
	Body           string         `json:"body"`
	IsDraft        bool           `json:"isDraft"`
	CreatedAt      string         `json:"createdAt"`
	UpdatedAt      string         `json:"updatedAt"`
	Author         *Actor         `json:"author"`
	HeadRefOid     string         `json:"headRefOid"`
	ReviewDecision string         `json:"reviewDecision"`
	Mergeable      string         `json:"mergeable"`
	Additions      int            `json:"additions"`
	Deletions      int            `json:"deletions"`
	ChangedFiles   int            `json:"changedFiles"`
	Labels         Labels         `json:"labels"`
	Files          Files          `json:"files"`
	ReviewRequests ReviewRequests `json:"reviewRequests"`
	Reviews        Reviews        `json:"reviews"`
	Commits        Commits        `json:"commits"`
	TimelineItems  Timeline       `json:"timelineItems"`
}
