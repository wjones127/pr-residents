"""GitHub GraphQL client (stdlib urllib only) and the pr-sync queries.

Auth is via a per-org token passed explicitly (no `gh` keyring, no env magic) so
behaviour is identical locally and in the remote routine sandbox.
"""

from __future__ import annotations

import json
import time
import urllib.error
import urllib.request
from typing import Any

_ENDPOINT = "https://api.github.com/graphql"

# Lightweight pass: just enough to decide which PRs changed since last sync.
_SEARCH_QUERY = """
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
}
"""

# Heavy pass: full detail for a single PR, fetched only on cache miss / change.
_DETAIL_QUERY = """
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number
      title
      url
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
        nodes { commit { oid committedDate statusCheckRollup { state } } }
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
}
"""


class GitHubError(RuntimeError):
    pass


class GitHubClient:
    def __init__(self, token: str, max_retries: int = 3):
        self.token = token
        self.max_retries = max_retries

    def graphql(self, query: str, variables: dict[str, Any]) -> dict[str, Any]:
        body = json.dumps({"query": query, "variables": variables}).encode("utf-8")
        req = urllib.request.Request(_ENDPOINT, data=body, method="POST")
        req.add_header("Authorization", f"Bearer {self.token}")
        req.add_header("Content-Type", "application/json")
        req.add_header("User-Agent", "pr-residents-sync")

        last_err: Exception | None = None
        for attempt in range(self.max_retries):
            try:
                with urllib.request.urlopen(req, timeout=30) as resp:
                    payload = json.loads(resp.read().decode("utf-8"))
                if payload.get("errors"):
                    raise GitHubError(json.dumps(payload["errors"]))
                return payload["data"]
            except urllib.error.HTTPError as exc:
                # Retry on rate-limit / transient 5xx; fail fast otherwise.
                if exc.code in (403, 429, 502, 503) and attempt < self.max_retries - 1:
                    last_err = exc
                    time.sleep(2 ** attempt)
                    continue
                detail = exc.read().decode("utf-8", "replace")
                raise GitHubError(f"HTTP {exc.code}: {detail}") from exc
            except urllib.error.URLError as exc:
                last_err = exc
                if attempt < self.max_retries - 1:
                    time.sleep(2 ** attempt)
                    continue
                raise GitHubError(str(exc)) from exc
        raise GitHubError(str(last_err))

    def viewer_login(self) -> str:
        data = self.graphql("query { viewer { login } }", {})
        return data["viewer"]["login"]

    def rest_get(self, path: str) -> Any:
        """GET a REST endpoint (path like '/repos/o/r/compare/a...b')."""
        url = "https://api.github.com" + path
        req = urllib.request.Request(url, method="GET")
        req.add_header("Authorization", f"Bearer {self.token}")
        req.add_header("Accept", "application/vnd.github+json")
        req.add_header("User-Agent", "pr-residents-sync")
        for attempt in range(self.max_retries):
            try:
                with urllib.request.urlopen(req, timeout=30) as resp:
                    return json.loads(resp.read().decode("utf-8"))
            except urllib.error.HTTPError as exc:
                if exc.code in (403, 429, 502, 503) and attempt < self.max_retries - 1:
                    time.sleep(2 ** attempt)
                    continue
                detail = exc.read().decode("utf-8", "replace")
                raise GitHubError(f"HTTP {exc.code}: {detail}") from exc
            except urllib.error.URLError as exc:
                if attempt < self.max_retries - 1:
                    time.sleep(2 ** attempt)
                    continue
                raise GitHubError(str(exc)) from exc
        raise GitHubError("rest_get exhausted retries")

    def compare(self, owner: str, name: str, base: str, head: str) -> dict[str, Any]:
        """Commit-anchored diff base...head (trap #2: anchor on the sha I last
        reviewed, not GitHub's built-in 'changes since')."""
        return self.rest_get(f"/repos/{owner}/{name}/compare/{base}...{head}")

    def search_light(self, query: str) -> list[dict[str, Any]]:
        """Return [{repo, number, updatedAt, headRefOid}] for a search query."""
        out: list[dict[str, Any]] = []
        cursor: str | None = None
        while True:
            data = self.graphql(_SEARCH_QUERY, {"q": query, "cursor": cursor})
            search = data["search"]
            for node in search["nodes"]:
                if not node:
                    continue
                out.append(
                    {
                        "repo": node["repository"]["nameWithOwner"],
                        "number": node["number"],
                        "updatedAt": node["updatedAt"],
                        "headRefOid": node["headRefOid"],
                    }
                )
            page = search["pageInfo"]
            if not page["hasNextPage"]:
                break
            cursor = page["endCursor"]
        return out

    def fetch_detail(self, owner: str, name: str, number: int) -> dict[str, Any]:
        data = self.graphql(
            _DETAIL_QUERY, {"owner": owner, "name": name, "number": number}
        )
        return data["repository"]["pullRequest"]
