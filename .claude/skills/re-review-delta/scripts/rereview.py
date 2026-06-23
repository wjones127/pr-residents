"""Produce a re-review packet for one PR: the reconstructed conditions ledger
plus the commit-anchored delta diff (last_reviewed_sha...head).

Deterministic, read-only. The resident (re-review-delta SKILL.md) consumes this
packet and assigns each condition met/not_met/moot by verifying against the
diff, then does the open-ended fresh-eyes pass. No status judgments here.

Usage:
    python3 rereview.py OWNER/REPO NUMBER [--out PATH]
"""

from __future__ import annotations

import argparse
import json
import os
import sys

# Reuse the pr-sync client + config (single source of truth for auth/GraphQL).
_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(_HERE, "..", "..", "pr-sync", "scripts"))

import config as config_mod  # noqa: E402
from github import GitHubClient, GitHubError  # noqa: E402

import conditions  # noqa: E402

MAX_PATCH_LINES = 500

_PR_QUERY = """
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number title url headRefOid
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
}
"""


def _last_reviewed_sha(reviews: list[dict], viewer: str) -> str | None:
    mine = [r for r in reviews
            if (r.get("author") or {}).get("login") == viewer and r.get("submittedAt")]
    if not mine:
        return None
    latest = max(mine, key=lambda r: r["submittedAt"])
    return (latest.get("commit") or {}).get("oid")


def _truncate_patch(patch: str | None) -> tuple[str | None, bool]:
    if not patch:
        return patch, False
    lines = patch.splitlines()
    if len(lines) <= MAX_PATCH_LINES:
        return patch, False
    return "\n".join(lines[:MAX_PATCH_LINES]), True


def build_packet(repo: str, number: int, config_dir: str) -> dict:
    owner, name = repo.split("/", 1)
    cfg = config_mod.load(config_dir)
    token = cfg.token_for(owner)
    if not token:
        raise GitHubError(f"${cfg.env_var_for(owner)} not set")
    client = GitHubClient(token)
    viewer = client.viewer_login()

    data = client.graphql(_PR_QUERY, {"owner": owner, "name": name, "number": number})
    pr = data["repository"]["pullRequest"]
    head = pr["headRefOid"]
    reviews = (pr.get("reviews") or {}).get("nodes") or []
    threads = (pr.get("reviewThreads") or {}).get("nodes") or []

    last_reviewed = _last_reviewed_sha(reviews, viewer)
    ledger = conditions.reconstruct(threads, viewer)

    delta: dict = {"base": last_reviewed, "head": head, "files": [], "commit_count": 0,
                   "note": None}
    if not last_reviewed:
        delta["note"] = "no prior review by viewer; nothing to anchor a delta on"
    elif last_reviewed == head:
        delta["note"] = "head unchanged since last review; delta is empty"
    else:
        cmp = client.compare(owner, name, last_reviewed, head)
        delta["commit_count"] = len(cmp.get("commits") or [])
        for f in cmp.get("files") or []:
            patch, truncated = _truncate_patch(f.get("patch"))
            delta["files"].append({
                "path": f.get("filename"),
                "status": f.get("status"),
                "additions": f.get("additions"),
                "deletions": f.get("deletions"),
                "patch": patch,
                "patch_truncated": truncated,
            })

    return {
        "pr": {"repo": repo, "number": number, "title": pr["title"], "url": pr["url"],
               "head": head, "last_reviewed_sha": last_reviewed, "viewer": viewer},
        "conditions": ledger,
        "delta": delta,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Build a re-review packet for one PR.")
    repo_root = os.path.abspath(os.path.join(_HERE, "..", "..", "..", ".."))
    parser.add_argument("repo", help="OWNER/REPO")
    parser.add_argument("number", type=int)
    parser.add_argument("--config-dir", default=os.path.join(repo_root, "config"))
    parser.add_argument("--out", default="-")
    args = parser.parse_args(argv)

    packet = build_packet(args.repo, args.number, args.config_dir)
    payload = json.dumps(packet, indent=2)
    if args.out == "-":
        print(payload)
    else:
        with open(args.out, "w", encoding="utf-8") as fh:
            fh.write(payload)
        print(f"[ok] wrote packet for {args.repo}#{args.number} "
              f"({len(packet['conditions'])} conditions, "
              f"{len(packet['delta']['files'])} delta files) to {args.out}",
              file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
