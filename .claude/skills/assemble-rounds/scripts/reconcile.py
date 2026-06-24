"""Reconciliation: diff last cycle's DRAFTED dispositions against what I actually
posted on GitHub, and fold the result into per-domain agreement / anchoring logs.

This is the graduated-autonomy instrument (§5): "the diff between what the
resident drafted and what you posted, per domain." We **log from day one even
though we won't act on it yet.** Nothing here writes to GitHub; it only reads my
reviews and updates per-user state.

The pure functions (classify_outcome / update_agreement / agreement_rate /
anchoring_flag) encode the §5 semantics and are exhaustively unit-tested. The I/O
layer reuses the pr-sync GitHub client.

Usage:
    python3 reconcile.py [--config-dir DIR] [--state-dir DIR]
"""

from __future__ import annotations

import argparse
import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(_HERE, "..", "..", "pr-sync", "scripts"))

import config as config_mod  # noqa: E402
import store as store_mod  # noqa: E402
from github import GitHubClient, GitHubError  # noqa: E402

# A domain needs at least this many reconciled samples before a flatlined
# dissent rate is suspicious rather than just sparse.
DEFAULT_MIN_SAMPLES = 8

_EMPTY = {"samples": 0, "agree": 0, "over_call": 0, "under_call": 0, "partial": 0}

_REVIEWS_QUERY = """
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviews(last: 50) { nodes { author { login } state submittedAt } }
    }
  }
}
"""


# --- pure semantics (§5) --------------------------------------------------

def classify_outcome(drafted: str, posted_state: str | None) -> str:
    """Compare a drafted disposition to the review I actually posted.

    Returns one of:
      agree       — posted review matches the draft's call
      over_call   — resident drafted block, I approved (too strict / timid)
      under_call  — resident drafted approve, I requested changes (missed a blocker)
      partial     — I only commented, or the draft wasn't an approve/block call
      no_action   — I haven't posted a review since the draft (nothing to learn)
    """
    d = (drafted or "").strip().lower()
    if posted_state is None:
        return "no_action"
    if d.startswith("approve"):
        if posted_state == "APPROVED":
            return "agree"
        if posted_state == "CHANGES_REQUESTED":
            return "under_call"
        return "partial"
    if d.startswith("block"):
        if posted_state == "CHANGES_REQUESTED":
            return "agree"
        if posted_state == "APPROVED":
            return "over_call"
        return "partial"
    return "partial"


def update_agreement(stats: dict | None, outcome: str) -> dict:
    """Fold one outcome into a domain's running stats. Pure — returns a new dict.
    `no_action` is not yet a data point, so it doesn't move the counters."""
    s = dict(stats) if stats else dict(_EMPTY)
    if outcome == "no_action":
        return s
    s["samples"] = s.get("samples", 0) + 1
    if outcome in ("agree", "over_call", "under_call", "partial"):
        s[outcome] = s.get(outcome, 0) + 1
    return s


def agreement_rate(stats: dict) -> float | None:
    n = stats.get("samples", 0)
    return None if n == 0 else round(stats.get("agree", 0) / n, 3)


def anchoring_flag(stats: dict, min_samples: int = DEFAULT_MIN_SAMPLES) -> bool:
    """§5 tell: in a domain where you SHOULD sometimes disagree, if dissent has
    flattened to zero over enough samples, flag possible anchoring. Instrument
    only — surfaced, never acted on."""
    n = stats.get("samples", 0)
    if n < min_samples:
        return False
    return (stats.get("over_call", 0) + stats.get("under_call", 0)) == 0


# --- I/O ------------------------------------------------------------------

def find_unreconciled(store, reconciled_cycles: set[str]) -> list[dict]:
    """Prior cycles' disposition files not yet folded in, oldest first."""
    out = []
    for key in store.list_keys(store_mod.DISPOSITIONS_PREFIX):
        blob = store.get_json(key)
        if not blob:
            continue
        if blob.get("cycle") and blob["cycle"] not in reconciled_cycles:
            out.append(blob)
    return out


def fetch_posted_state(client: GitHubClient, owner: str, name: str, number: int,
                       viewer: str, since_iso: str) -> str | None:
    """My latest review STATE on this PR submitted after the draft, or None."""
    data = client.graphql(_REVIEWS_QUERY, {"owner": owner, "name": name, "number": number})
    nodes = (((data.get("repository") or {}).get("pullRequest") or {})
             .get("reviews") or {}).get("nodes") or []
    mine = [r for r in nodes
            if (r.get("author") or {}).get("login") == viewer
            and r.get("submittedAt") and r["submittedAt"] > since_iso]
    if not mine:
        return None
    return max(mine, key=lambda r: r["submittedAt"]).get("state")


def _load_log(store) -> dict:
    return store.get_json(store_mod.RECONCILE_AGREEMENT) or {
        "domains": {}, "reconciled_cycles": []}


def run(config_dir: str, state_dir: str, min_samples: int = DEFAULT_MIN_SAMPLES) -> dict:
    cfg = config_mod.load(config_dir)
    store = store_mod.FileStore(state_dir)
    log = _load_log(store)
    reconciled = set(log.get("reconciled_cycles") or [])

    pending = find_unreconciled(store, reconciled)
    if not pending:
        return {"reconciled": 0, "note": "no unreconciled cycles", "log": log}

    clients: dict[str, GitHubClient] = {}
    viewers: dict[str, str] = {}
    processed = 0
    for cycle in pending:
        for d in cycle.get("dispositions") or []:
            repo = d.get("repo", "")
            if "/" not in repo:
                continue
            owner, name = repo.split("/", 1)
            if owner not in clients:
                token = cfg.token_for(owner)
                if not token:
                    continue
                try:
                    clients[owner] = GitHubClient(token)
                    viewers[owner] = clients[owner].viewer_login()
                except GitHubError:
                    continue
            try:
                posted = fetch_posted_state(
                    clients[owner], owner, name, d["number"],
                    viewers[owner], d.get("drafted_at", ""))
            except GitHubError:
                continue
            outcome = classify_outcome(d.get("recommendation", ""), posted)
            domain = d.get("domain") or "unknown"
            log["domains"][domain] = update_agreement(log["domains"].get(domain), outcome)
        log.setdefault("reconciled_cycles", []).append(cycle["cycle"])
        processed += 1

    log["anchoring_flags"] = sorted(
        dom for dom, s in log["domains"].items() if anchoring_flag(s, min_samples))

    store.put_json(store_mod.RECONCILE_AGREEMENT, log)
    return {"reconciled": processed, "log": log}


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Reconcile drafted vs posted dispositions.")
    repo_root = os.path.abspath(os.path.join(_HERE, "..", "..", "..", ".."))
    parser.add_argument("--config-dir", default=os.path.join(repo_root, "config"))
    parser.add_argument("--state-dir", default=os.path.join(repo_root, "state"))
    parser.add_argument("--min-samples", type=int, default=DEFAULT_MIN_SAMPLES)
    args = parser.parse_args(argv)

    result = run(args.config_dir, args.state_dir, args.min_samples)
    domains = result["log"].get("domains", {})
    print(f"[ok] reconciled {result['reconciled']} cycle(s); "
          f"{len(domains)} domain(s) tracked", file=sys.stderr)
    for dom, s in sorted(domains.items()):
        rate = agreement_rate(s)
        flag = " ⚑anchoring?" if dom in (result["log"].get("anchoring_flags") or []) else ""
        print(f"  {dom}: agree {rate} over {s['samples']} "
              f"(over-call {s['over_call']}, under-call {s['under_call']}){flag}",
              file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
