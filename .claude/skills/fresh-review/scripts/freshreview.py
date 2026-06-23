"""Produce a fresh-review packet for one PR: the full net diff (patches) plus the
deterministic PRRecord baseline (acuity / effort / escalation / author_status).

Deterministic, read-only. The resident (fresh-review SKILL.md) consumes this and
produces a SOAP review — findings tied to ground truth, drafted conventional
comments, an approve/block recommendation. No review judgments are made here.

The fresh-lane sibling of re-review-delta: there is no prior conditions ledger,
so the resident reads the whole net diff rather than a delta.

Usage:
    python3 freshreview.py OWNER/REPO NUMBER [--out PATH]
"""

from __future__ import annotations

import argparse
import json
import os
import sys

# Reuse the pr-sync client + config + derivations (single source of truth).
_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(_HERE, "..", "..", "pr-sync", "scripts"))

import config as config_mod  # noqa: E402
import derive  # noqa: E402
from github import GitHubClient, GitHubError  # noqa: E402

MAX_PATCH_LINES = 500


def partition_files(files: list[dict]) -> tuple[list[dict], list[str]]:
    """Split into (reviewable, omitted_paths). GitHub omits `patch` for binary or
    very large files; the resident must know what it could NOT read rather than
    silently skipping it."""
    reviewable = [f for f in files if f.get("patch")]
    omitted = [f.get("filename") for f in files if not f.get("patch")]
    return reviewable, omitted


def _truncate_patch(patch: str | None) -> tuple[str | None, bool]:
    if not patch:
        return patch, False
    lines = patch.splitlines()
    if len(lines) <= MAX_PATCH_LINES:
        return patch, False
    return "\n".join(lines[:MAX_PATCH_LINES]), True


def _author_merged_count(client: GitHubClient, repo: str, author: str | None,
                         viewer: str) -> int | None:
    if not author or author == viewer:
        return None
    try:
        return client.search_count(f"repo:{repo} is:pr is:merged author:{author}")
    except GitHubError:
        return None


def build_packet(repo: str, number: int, config_dir: str) -> dict:
    owner, name = repo.split("/", 1)
    cfg = config_mod.load(config_dir)
    token = cfg.token_for(owner)
    if not token:
        raise GitHubError(f"${cfg.env_var_for(owner)} not set")
    client = GitHubClient(token)
    viewer = client.viewer_login()

    detail = client.fetch_detail(owner, name, number)
    author = (detail.get("author") or {}).get("login")
    merged_count = _author_merged_count(client, repo, author, viewer)

    # requested=True: I am choosing to review this PR, so derive it as owed-to-me
    # rather than letting the surfacing gate drop it. None only for my own PRs.
    record = derive.build_record(detail, viewer, True, cfg.escalation,
                                 author_merged_count=merged_count)
    if record is None:
        raise GitHubError(f"{repo}#{number} is your own PR — not a review target")

    files = client.pull_files_full(owner, name, number)
    reviewable, omitted = partition_files(files)
    diff_files = []
    for f in reviewable:
        patch, truncated = _truncate_patch(f.get("patch"))
        diff_files.append({
            "path": f.get("filename"),
            "status": f.get("status"),
            "additions": f.get("additions"),
            "deletions": f.get("deletions"),
            "patch": patch,
            "patch_truncated": truncated,
        })

    lane_note = None
    if record["lane"] != "fresh":
        lane_note = (
            f"this PR is in the {record['lane']} lane "
            f"(blocked_on={record['blocked_on']}) — you have a prior review, so "
            f"re-review-delta is the right resident. Packet built anyway for a "
            f"full re-read.")

    return {
        "pr": {
            "repo": repo, "number": number, "title": record["title"],
            "url": record["url"], "head": detail["headRefOid"], "author": author,
            "author_status": record["author_status"], "body": record["body"],
            "lane": record["lane"], "blocked_on": record["blocked_on"],
        },
        "acuity": record["acuity"],
        "effort": record["effort"],
        "escalation": record["escalation"],
        "merge_state": record["merge_state"],
        "diff": {
            "files": diff_files,
            "omitted": omitted,
            "total_files": len(files),
            "shown_files": len(diff_files),
        },
        "lane_note": lane_note,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Build a fresh-review packet for one PR.")
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
        d = packet["diff"]
        print(f"[ok] wrote fresh-review packet for {args.repo}#{args.number} "
              f"({d['shown_files']}/{d['total_files']} files, "
              f"{len(d['omitted'])} omitted) to {args.out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
