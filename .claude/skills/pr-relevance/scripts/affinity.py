"""Pure scoring for pr-relevance: a path-affinity profile built from the PRs I
have reviewed, candidate scoring against it, and a cold-start fallback for when
my history is too thin to trust.

No network, no LLM, no config loading (mirrors derive.py). relevance.py wires in
the GitHub fetches, config interests, and escalation rules; this module is just
the math, so it is exhaustively unit-testable.

This is the *no-ML* half of §8's relevance ranker (path-overlap, per-repo). The
semantic k-NN half (embeddings of title+desc, global) is a documented extension
point — see SKILL.md — that switches on once history accumulates.
"""

from __future__ import annotations

DEFAULT_BUCKET_DEPTH = 2
# Below this many reviewed PRs in a repo, path-affinity is noise — fall back to
# the cold-start signals (declared interests + hard-escalation paths).
MIN_HISTORY = 5


def bucket_path(path: str, depth: int = DEFAULT_BUCKET_DEPTH) -> str:
    """A coarse "area" key: the first `depth` path segments — a crate or top dir.
    e.g. rust/lance-encoding/src/x.rs -> rust/lance-encoding (depth 2). A file at
    or near the root buckets by its directory (or the filename if at root)."""
    parts = [p for p in path.split("/") if p]
    if not parts:
        return ""
    if len(parts) <= depth:
        return "/".join(parts[:-1]) or parts[0]
    return "/".join(parts[:depth])


def buckets_for(paths, depth: int = DEFAULT_BUCKET_DEPTH) -> set[str]:
    return {bucket_path(p, depth) for p in paths if p}


def primary_domain(paths, depth: int = DEFAULT_BUCKET_DEPTH) -> str:
    """The PR's dominant area: the bucket most of its changed files fall under.
    This is the canonical `domain` for a §5 disposition — derived deterministically
    from the changed files (the same bucketing the affinity profile uses) rather
    than free-typed by a resident, so the per-domain agreement log doesn't
    fragment on `lance-arrow` vs `rust/lance-arrow`. Ties break alphabetically for
    a stable key. Returns "" for an empty/path-less change set."""
    counts: dict[str, int] = {}
    for p in paths or []:
        b = bucket_path(p, depth)
        if b:
            counts[b] = counts.get(b, 0) + 1
    if not counts:
        return ""
    top = max(counts.values())
    return sorted(b for b, c in counts.items() if c == top)[0]


def build_profile(reviewed_paths_per_pr, depth: int = DEFAULT_BUCKET_DEPTH) -> dict[str, int]:
    """`reviewed_paths_per_pr`: one path-list per PR I have reviewed. Returns
    {area: how many of my reviewed PRs touched it}. Counting PRs (not files)
    keeps a single sprawling PR from dominating the profile."""
    weights: dict[str, int] = {}
    for paths in reviewed_paths_per_pr:
        for b in buckets_for(paths, depth):
            weights[b] = weights.get(b, 0) + 1
    return weights


def score_candidate(candidate_paths, profile: dict[str, int],
                    depth: int = DEFAULT_BUCKET_DEPTH):
    """Score a candidate PR by overlap of its areas with my affinity profile.
    Returns (score, matched) where matched is [(area, weight), ...] desc — the
    raw material for a human-readable rationale."""
    cand_buckets = buckets_for(candidate_paths, depth)
    matched = sorted(
        ((b, profile[b]) for b in cand_buckets if b in profile),
        key=lambda kv: (-kv[1], kv[0]),
    )
    score = float(sum(w for _, w in matched))
    return score, matched


def affinity_rationale(matched) -> str:
    if not matched:
        return "no overlap with areas you've reviewed"
    top = ", ".join(f"{b} (reviewed {w}×)" for b, w in matched[:3])
    return "overlaps your review history: " + top


def cold_start_score(candidate_paths, interests, escalation_rule_ids):
    """When review history is too thin to trust path-affinity, rank on the
    *shared* signals a new teammate already has (§9): declared interests +
    hard-escalation paths. Returns (score, rationale). `escalation_rule_ids` is
    computed by the caller (relevance.py) from config/escalation.yml."""
    matched_interests = sorted({
        i for i in (interests or []) for p in candidate_paths
        if p == i or p.startswith(i.rstrip("/") + "/")
    })
    rule_ids = list(escalation_rule_ids or [])
    score = float(len(matched_interests) + len(rule_ids))

    bits = []
    if matched_interests:
        bits.append("matches your interests: " + ", ".join(matched_interests))
    if rule_ids:
        bits.append("touches escalation paths: " + ", ".join(rule_ids))
    tail = "; ".join(bits) if bits else "no shared-signal match"
    return score, "cold-start (thin history) — " + tail
