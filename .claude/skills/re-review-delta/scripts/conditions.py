"""Reconstruct the conditions ledger from posted review comments.

Deterministic parse of conventional-comment review threads (see
config/comment-vocab.md) into the PRRecord `conditions` shape. This is the
"reconstructed each run, not stored" half (trap #4). Status stays `open` here —
verifying met/not_met/moot against the diff is the resident's job
(re-review-delta SKILL.md), because GitHub "resolved" is only a claim (trap #3).
"""

from __future__ import annotations

import re
from typing import Any

# `label(decoration): subject`, tolerant of leading markdown (**, _, >, spaces).
_LABEL = re.compile(
    r"^[\s>*_~`]*"
    r"(?P<label>issue|suggestion|question|nitpick|praise|todo|thought)"
    r"\s*(?:\(\s*(?P<decoration>[a-z -]+?)\s*\))?"
    r"[\s*_]*:",
    re.IGNORECASE,
)

# Labels that produce a ledger entry (binding or tracked). Others (question,
# nitpick, praise, todo, thought) are not conditions.
_CONDITION_LABELS = {"issue", "suggestion"}


def parse_label(body: str) -> tuple[str, str | None] | None:
    """Return (label, decoration) from a comment body, or None if unlabeled."""
    if not body:
        return None
    first = body.strip().splitlines()[0] if body.strip() else ""
    m = _LABEL.match(first)
    if not m:
        return None
    decoration = m.group("decoration")
    return (m.group("label").lower(), decoration.lower().strip() if decoration else None)


def classify_kind(label: str, decoration: str | None) -> str | None:
    """Map a conventional label to a PRRecord condition kind, or None."""
    if label == "suggestion":
        return "suggestion"
    if label == "issue":
        if decoration == "non-blocking":
            return "non_blocking"
        # blocking, or bare `issue:` -> treat as blocking (conservative).
        return "blocking"
    return None


def _subject(body: str) -> str:
    first = body.strip().splitlines()[0] if body.strip() else ""
    m = _LABEL.match(first)
    text = first[m.end():].strip() if m else first
    return text


def reconstruct(threads: list[dict], viewer: str) -> list[dict[str, Any]]:
    """Build the conditions ledger from review threads authored by `viewer`."""
    ledger: list[dict[str, Any]] = []
    for thread in threads:
        comments = (thread.get("comments") or {}).get("nodes") or []
        if not comments:
            continue
        root = comments[0]
        author = (root.get("author") or {}).get("login")
        if author != viewer:
            continue
        parsed = parse_label(root.get("body") or "")
        if parsed is None:
            continue
        label, decoration = parsed
        if label not in _CONDITION_LABELS:
            continue
        kind = classify_kind(label, decoration)
        if kind is None:
            continue
        line = thread.get("line") or thread.get("originalLine")
        path = thread.get("path")
        ledger.append({
            "id": thread.get("id"),
            "text": _subject(root.get("body") or ""),
            "kind": kind,
            "status": "open",          # resident sets met/not_met/moot
            "evidence_ref": None,      # resident fills with verification evidence
            "location": f"{path}:{line}" if path else None,
            "author_resolved": bool(thread.get("isResolved")),
            "is_outdated": bool(thread.get("isOutdated")),
        })
    return ledger
