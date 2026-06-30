"""Derive a PRRecord from raw GitHub detail (see docs/prrecord.md).

Pure functions over the detail dict + viewer identity + escalation rules. No
network, no LLM. This is the deterministic baseline the residents later refine.
"""

from __future__ import annotations

import re

# Bump when derivation logic changes in a way that should invalidate the cache.
# 6: drafts route to the author path (housekeeping-if-stale, never fresh/re_review).
DERIVE_VERSION = "6"
from datetime import datetime, timezone
from typing import Any

# Changed-file path types treated as inherently low risk for the baseline.
_LOW_RISK_PATTERNS = [
    "*.md", "*.txt", "*.rst", "docs/**", "**/docs/**",
    "**/test_*", "**/*_test.*", "**/tests/**", "*.lock",
    "**/Cargo.lock", "**/*.toml", "**/.github/**",
]

_CI_MAP = {
    "SUCCESS": "green",
    "FAILURE": "red",
    "ERROR": "red",
    "PENDING": "pending",
    "EXPECTED": "pending",
}


def merge_state_from_detail(detail: dict) -> dict:
    """CI rollup + mergeability from a PR detail, as {"ci", "mergeable"}. Reads
    the latest commit's statusCheckRollup and the PR's mergeable flag — the same
    shape build_record stores. Factored out so a re-review packet can carry CI
    fetched live at build time instead of leaning on the (possibly hours-old)
    synced record, which is what made residents re-fetch CI by hand."""
    commit_nodes = (detail.get("commits") or {}).get("nodes") or []
    rollup = (commit_nodes[0].get("commit") or {}).get("statusCheckRollup") if commit_nodes else None
    ci = _CI_MAP.get(rollup.get("state") if rollup else None, "pending")
    mergeable = {"MERGEABLE": True, "CONFLICTING": False}.get(detail.get("mergeable"))
    return {"ci": ci, "mergeable": mergeable}


# --- glob matching --------------------------------------------------------

def _glob_to_regex(pattern: str) -> str:
    # `**/` matches zero or more leading path segments; `*` stays within a segment.
    out = []
    i = 0
    while i < len(pattern):
        if pattern.startswith("**/", i):
            out.append(r"(?:.*/)?")
            i += 3
        elif pattern.startswith("**", i):
            out.append(r".*")
            i += 2
        elif pattern[i] == "*":
            out.append(r"[^/]*")
            i += 1
        else:
            out.append(re.escape(pattern[i]))
            i += 1
    return "^" + "".join(out) + "$"


def path_matches(path: str, pattern: str) -> bool:
    return re.match(_glob_to_regex(pattern), path) is not None


def _any_path_matches(paths: list[str], patterns: list[str]) -> bool:
    return any(path_matches(p, pat) for p in paths for pat in patterns)


# --- helpers --------------------------------------------------------------

def _parse_ts(value: str | None) -> datetime | None:
    if not value:
        return None
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def _hours_since(value: str | None, now: datetime) -> float:
    ts = _parse_ts(value)
    if ts is None:
        return 0.0
    return round((now - ts).total_seconds() / 3600.0, 1)


def _my_latest_review(reviews: list[dict], viewer: str) -> dict | None:
    mine = [r for r in reviews if (r.get("author") or {}).get("login") == viewer]
    mine = [r for r in mine if r.get("submittedAt")]
    if not mine:
        return None
    return max(mine, key=lambda r: r["submittedAt"])


def _head_arrived_at(detail: dict) -> str | None:
    """Latest of head commit date and any force-push event (trap #1 fix:
    ordering/identity, never pushedDate)."""
    times: list[str] = []
    commit_nodes = (detail.get("commits") or {}).get("nodes") or []
    if commit_nodes:
        cd = (commit_nodes[0].get("commit") or {}).get("committedDate")
        if cd:
            times.append(cd)
    for node in (detail.get("timelineItems") or {}).get("nodes") or []:
        if node.get("__typename") == "HeadRefForcePushedEvent" and node.get("createdAt"):
            times.append(node["createdAt"])
    return max(times) if times else None


# --- escalation -----------------------------------------------------------

def match_escalation(paths: list[str], labels: list[str], total_lines: int,
                     rules: dict[str, Any]) -> dict[str, Any]:
    rule_ids: list[str] = []
    reasons: list[str] = []
    for rule in rules.get("path_rules") or []:
        if _any_path_matches(paths, rule.get("any_path_matches") or []):
            rule_ids.append(rule["id"])
            reasons.append(rule["reason"])
    label_set = {l.lower() for l in labels}
    for rule in rules.get("label_rules") or []:
        if any(l.lower() in label_set for l in rule.get("any_label_matches") or []):
            rule_ids.append(rule["id"])
            reasons.append(rule["reason"])
    for rule in rules.get("size_rules") or []:
        if total_lines >= int(rule.get("min_total_lines", 1 << 30)):
            rule_ids.append(rule["id"])
            reasons.append(rule["reason"])
    return {
        "forced": bool(rule_ids),
        "rule_ids": rule_ids,
        "reason": "; ".join(reasons),
    }


# --- axis derivations -----------------------------------------------------

def derive_effort(additions: int, deletions: int, files: int) -> dict[str, Any]:
    total = additions + deletions
    if total <= 10:
        bucket = "XS"
    elif total <= 50:
        bucket = "S"
    elif total <= 200:
        bucket = "M"
    elif total <= 600:
        bucket = "L"
    else:
        bucket = "XL"
    return {
        "size_bucket": bucket,
        "additions": additions,
        "deletions": deletions,
        "files": files,
    }


def derive_author_status(merged_count: int | None) -> str:
    """Contributor familiarity from prior merged PRs in this repo (triage input).
    `None` means we couldn't count (search failed) -> 'unknown', never guessed."""
    if merged_count is None:
        return "unknown"
    if merged_count == 0:
        return "first_time"
    if merged_count <= 3:
        return "infrequent"
    if merged_count <= 10:
        return "regular"
    return "core"


def derive_blocked_on(detail: dict, viewer: str, requested: bool) -> tuple[str, dict | None]:
    """Return (blocked_on, my_latest_review). Pure SHA-identity logic (trap #1)."""
    head = detail.get("headRefOid")
    reviews = (detail.get("reviews") or {}).get("nodes") or []
    latest = _my_latest_review(reviews, viewer)

    if detail.get("isDraft"):
        # A draft is the author saying "not ready," whatever the review history
        # says — the ball is in their court. Route to the author path so it lands
        # in housekeeping-if-stale, never fresh/re_review: spending a SOAP on a
        # PR the author hasn't opened for review is wasted on a moving target.
        return ("author", latest)

    if latest is None:
        return ("me" if requested else "other_reviewer", None)

    reviewed_oid = (latest.get("commit") or {}).get("oid")
    state = latest.get("state")

    if reviewed_oid and reviewed_oid != head:
        # Author pushed since my review -> re-review owed to me (covers the
        # stale-approval-reopened case too).
        return ("me", latest)
    if state == "APPROVED":
        return ("merge", latest)
    if state == "CHANGES_REQUESTED":
        return ("author", latest)
    if state == "COMMENTED":
        return ("me" if requested else "author", latest)
    return ("other_reviewer", latest)


def derive_acuity(paths: list[str], blocked_on: str, age_hrs: float,
                  ci: str) -> dict[str, Any]:
    # Risk is scored on the change itself, INDEPENDENT of hard-escalation
    # (plan §5: escalation routes to full rounds, it is not a risk score).
    if paths and all(
        any(path_matches(p, pat) for pat in _LOW_RISK_PATTERNS) for p in paths
    ):
        risk = "low"
        rationale = "docs/tests/config/deps only"
    else:
        risk = "med"
        rationale = "core library change; resident to refine from diff content"

    if blocked_on == "merge" and ci == "red":
        urgency = "high"
    elif blocked_on == "me" and age_hrs > 72:
        urgency = "high"
    elif age_hrs > 48:
        urgency = "med"
    else:
        urgency = "low"
    return {"risk": risk, "urgency": urgency, "rationale": rationale}


def derive_lane(blocked_on: str, last_reviewed_sha: str | None,
                delta: dict | None, age_hrs: float,
                stale_author_hrs: float = 48.0) -> str | None:
    # Lane is purely blocked_on-driven. Escalation does NOT override it:
    # fresh and re_review are both full-attention lanes, and collapsing
    # re_review into fresh would drop the conditions ledger for exactly the
    # high-stakes PRs that need it. Escalation only prevents fast-tracking
    # (a slice-5 concern) and sets the ⚠ flag.
    if blocked_on == "me":
        if last_reviewed_sha and delta:
            return "re_review"
        return "fresh"
    if blocked_on == "merge":
        return "housekeeping"
    if blocked_on == "author":
        return "housekeeping" if age_hrs >= stale_author_hrs else None
    return None  # other_reviewer: not surfaced


# --- top-level ------------------------------------------------------------

def build_record(detail: dict, viewer: str, requested: bool,
                 escalation_rules: dict[str, Any], now: datetime | None = None,
                 author_merged_count: int | None = None,
                 ) -> dict[str, Any] | None:
    """Build a PRRecord dict, or None if the PR should not be surfaced."""
    now = now or datetime.now(timezone.utc)
    author = (detail.get("author") or {}).get("login")
    if author == viewer:
        # Never surface my own PRs for review. Tracking my own work is a
        # separate concern with an inverted blocked_on framing (see docs).
        return None
    repo = None  # filled by caller context; detail has no nameWithOwner here
    head = detail.get("headRefOid")
    files = [f["path"] for f in (detail.get("files") or {}).get("nodes") or []]
    labels = [l["name"] for l in (detail.get("labels") or {}).get("nodes") or []]
    additions = detail.get("additions") or 0
    deletions = detail.get("deletions") or 0
    total_lines = additions + deletions

    escalation = match_escalation(files, labels, total_lines, escalation_rules)
    blocked_on, latest_review = derive_blocked_on(detail, viewer, requested)

    last_reviewed_sha = (
        (latest_review.get("commit") or {}).get("oid") if latest_review else None
    )
    delta = None
    if last_reviewed_sha and last_reviewed_sha != head:
        delta = {"commits": [head], "files_touched": files}

    # age_in_state: time since the event that put it in this state.
    if blocked_on == "author" and latest_review:
        gov_ts = latest_review.get("submittedAt")
    elif blocked_on == "merge" and latest_review:
        gov_ts = latest_review.get("submittedAt")
    else:  # me / other -> since head arrived
        gov_ts = _head_arrived_at(detail)
    age_hrs = _hours_since(gov_ts, now)

    ms = merge_state_from_detail(detail)

    lane = derive_lane(blocked_on, last_reviewed_sha, delta, age_hrs)
    if lane is None:
        return None

    acuity = derive_acuity(files, blocked_on, age_hrs, ms["ci"])

    return {
        "repo": repo,
        "number": detail["number"],
        "url": detail["url"],
        "title": detail["title"],
        "body": detail.get("body") or "",
        "author": author,
        "author_status": derive_author_status(author_merged_count),
        "files_changed": files,
        "blocked_on": blocked_on,
        "is_draft": bool(detail.get("isDraft")),
        "age_in_state_hrs": age_hrs,
        "lane": lane,
        "acuity": acuity,
        "effort": derive_effort(additions, deletions, detail.get("changedFiles") or 0),
        "relevance": {"score": None, "requested": requested},
        "last_reviewed_sha": last_reviewed_sha,
        "delta": delta,
        "conditions": [],  # reconstructed in slice 2 (re-review-delta)
        "merge_state": ms,
        "escalation": escalation,
    }
