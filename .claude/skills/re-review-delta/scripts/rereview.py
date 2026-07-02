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
import derive  # noqa: E402
from github import GitHubClient, GitHubError  # noqa: E402

import conditions  # noqa: E402

MAX_PATCH_LINES = 500

_PR_QUERY = """
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      number title url headRefOid mergeable
      commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
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
    mine = [
        r
        for r in reviews
        if (r.get("author") or {}).get("login") == viewer and r.get("submittedAt")
    ]
    if not mine:
        return None
    latest = max(mine, key=lambda r: r["submittedAt"])
    return (latest.get("commit") or {}).get("oid")


def scope_delta_files(
    compare_files: list[dict],
    pr_net_paths: set[str],
) -> tuple[list[dict], int]:
    """Keep only files that are part of the PR's own net diff. Drops churn a
    long-lived branch picked up by merging its base branch in. Returns
    (kept_files, excluded_count)."""
    kept = [f for f in compare_files if f.get("filename") in pr_net_paths]
    return kept, len(compare_files) - len(kept)


def _truncate_patch(patch: str | None) -> tuple[str | None, bool]:
    if not patch:
        return patch, False
    lines = patch.splitlines()
    if len(lines) <= MAX_PATCH_LINES:
        return patch, False
    return "\n".join(lines[:MAX_PATCH_LINES]), True


def build_delta(
    last_reviewed: str | None, head: str, cmp: dict | None, pr_net_full: list[dict]
) -> dict:
    """Assemble the re-review delta from already-fetched inputs. Pure + testable
    (no network): `cmp` is the compare(last_reviewed...head) payload (None when
    there's no anchor or head is unchanged); `pr_net_full` is pull_files_full —
    the PR's net changed files WITH patches (3-dot vs the merge base).

    The commit-anchored delta (compare base...head) is only valid when
    `last_reviewed` is an ANCESTOR of head. A rebase / force-push / squash
    rewrites history and orphans the anchor: GitHub then reports the compare as
    `status != "ahead"` and anchors the diff on an ancient merge base, sweeping
    in the whole divergent base history (hundreds of files) and omitting patches
    on most of them — so the author's own files come back with a null patch and
    0/0 counts. In that case there is no honest "since you last looked" delta, so
    we degrade to the full PR net diff and flag `anchor_orphaned` — the resident
    verifies the conditions ledger fresh-eyes over the whole PR (the ledger is
    reconstructed from review threads, which survive a rebase)."""
    delta: dict = {
        "base": last_reviewed,
        "head": head,
        "files": [],
        "commit_count": 0,
        "files_off_branch_excluded": 0,
        "anchor_orphaned": False,
        "compare_status": None,
        "note": None,
    }
    if not last_reviewed:
        delta["note"] = "no prior review by viewer; nothing to anchor a delta on"
        return delta
    if last_reviewed == head:
        delta["note"] = "head unchanged since last review; delta is empty"
        return delta

    pr_net_by_path = {f.get("filename"): f for f in pr_net_full}
    status = (cmp or {}).get("status")
    delta["compare_status"] = status
    delta["commit_count"] = len((cmp or {}).get("commits") or [])

    if status == "ahead":
        # Clean linear progress: last_reviewed IS an ancestor of head. Scope the
        # since-last-review compare files to the author's own net changes so a
        # branch that merged main in isn't swamped by main's churn.
        kept, excluded = scope_delta_files(
            (cmp or {}).get("files") or [], set(pr_net_by_path)
        )
        delta["files_off_branch_excluded"] = excluded
        if excluded:
            delta["note"] = (
                f"scoped to author's changes: excluded {excluded} files that "
                f"changed on the branch via merges from the base branch, not the "
                f"PR's own work"
            )
    else:
        # Orphaned anchor (diverged / behind / identical-but-different-sha):
        # no valid delta exists. Fall back to the full PR net diff.
        delta["anchor_orphaned"] = True
        kept = list(pr_net_full)
        delta["note"] = (
            f"prior-review anchor {last_reviewed[:8]} is orphaned (compare "
            f"status={status}: branch rebased/force-pushed onto a new base) — no "
            f"scoped delta available; showing full PR net diff, verify conditions "
            f"fresh-eyes"
        )

    for f in kept:
        patch = f.get("patch")
        if not patch:
            # GitHub omits `patch` for binary/large files and for ANY file when
            # the compare range is huge (the orphaned-anchor case). Backfill from
            # the PR net diff, which carries the author's real patch + counts.
            alt = pr_net_by_path.get(f.get("filename"))
            if alt and alt.get("patch"):
                f = alt
                patch = f.get("patch")
        patch, truncated = _truncate_patch(patch)
        delta["files"].append(
            {
                "path": f.get("filename"),
                "status": f.get("status"),
                "additions": f.get("additions"),
                "deletions": f.get("deletions"),
                "patch": patch,
                "patch_truncated": truncated,
            }
        )
    return delta


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

    # Fetch the compare + net diff only when there's a moved anchor to diff. The
    # net diff (pull_files_full) doubles as both the scoping set and the fallback
    # patch source when the compare anchor is orphaned by a rebase (build_delta).
    cmp = None
    pr_net_full: list[dict] = []
    if last_reviewed and last_reviewed != head:
        cmp = client.compare(owner, name, last_reviewed, head)
        pr_net_full = client.pull_files_full(owner, name, number)
    delta = build_delta(last_reviewed, head, cmp, pr_net_full)

    return {
        "pr": {
            "repo": repo,
            "number": number,
            "title": pr["title"],
            "url": pr["url"],
            "head": head,
            "last_reviewed_sha": last_reviewed,
            "viewer": viewer,
        },
        # CI/mergeability fetched live here at build time — don't re-fetch by hand;
        # the synced record's copy may be hours stale (parity with the fresh packet).
        "merge_state": derive.merge_state_from_detail(pr),
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
        print(
            f"[ok] wrote packet for {args.repo}#{args.number} "
            f"({len(packet['conditions'])} conditions, "
            f"{len(packet['delta']['files'])} delta files) to {args.out}",
            file=sys.stderr,
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
