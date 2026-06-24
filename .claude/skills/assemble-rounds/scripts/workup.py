"""Workup cache: a resident's SOAP keyed on the PR's head SHA.

The single biggest token lever (§ token review): a SOAP review costs ~15–35K
tokens to produce. If the PR's `headRefOid` hasn't moved since we last reviewed
it, the prior SOAP is still exactly valid — so the orchestrator can reuse it and
spawn no subagent at all. The cache key is the head SHA, because a force-push
(new SHA) is precisely what invalidates a review (§3).

This rides the Store seam (cache/ namespace) so it survives a remote run via the
git round-trip — a bare `state/` file would silently no-op there.

Usage (the orchestrator shells out to this):
    workup.py get --repo owner/name --number 7416 --sha <oid>
        -> prints cached SOAP to stdout, exit 0; exit 3 on miss (no subagent? spawn one)
    workup.py put --repo owner/name --number 7416 --sha <oid> --at <iso> < soap.md
        -> stores SOAP read from stdin
"""

from __future__ import annotations

import argparse
import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(_HERE, "..", "..", "pr-sync", "scripts"))

import store as store_mod  # noqa: E402

MISS = 3


def get_workup(store, repo: str, number: int, sha: str) -> dict | None:
    """The cached SOAP for this exact head SHA, or None if absent/stale."""
    return store.get_json(store_mod.workup_key(repo, number, sha))


def put_workup(store, repo: str, number: int, sha: str, soap: str,
               cached_at: str = "") -> str:
    """Cache a SOAP against its head SHA. Returns the key written."""
    key = store_mod.workup_key(repo, number, sha)
    store.put_json(key, {
        "repo": repo, "number": number, "sha": sha,
        "soap": soap, "cached_at": cached_at,
    })
    return key


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Workup SOAP cache, keyed on head SHA.")
    parser.add_argument("op", choices=["get", "put"])
    parser.add_argument("--repo", required=True, help="owner/name")
    parser.add_argument("--number", type=int, required=True)
    parser.add_argument("--sha", required=True, help="PR headRefOid")
    parser.add_argument("--at", default="", help="UTC timestamp for put (pass it in)")
    parser.add_argument("--state-dir", default=None)
    args = parser.parse_args(argv)

    store = store_mod.FileStore(args.state_dir)

    if args.op == "get":
        hit = get_workup(store, args.repo, args.number, args.sha)
        if hit is None:
            print(f"[miss] {args.repo}#{args.number}@{args.sha[:8]}", file=sys.stderr)
            return MISS
        sys.stdout.write(hit.get("soap", ""))
        return 0

    soap = sys.stdin.read()
    key = put_workup(store, args.repo, args.number, args.sha, soap, args.at)
    print(f"[ok] cached {key}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
