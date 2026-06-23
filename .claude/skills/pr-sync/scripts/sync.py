"""pr-sync: fetch review-relevant PRs across repos and emit PRRecord JSON.

Deterministic, no LLM. Owns the docs/prrecord.md correctness traps. Per-org
read-only tokens come from env (GITHUB_TOKEN_<ORG>). Stdlib only.

Usage:
    python3 sync.py [--config-dir DIR] [--cache PATH] [--out PATH]
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import sys
from datetime import datetime, timezone

import config as config_mod
import derive
from cache import Cache
from github import GitHubClient, GitHubError

# Search qualifiers per relevance category. `@me` resolves to the token owner.
# `-author:@me` excludes my own PRs — reviewing them is not this tool's job.
_CATEGORIES = {
    "requested": "review-requested:@me -author:@me",
    "reviewed": "reviewed-by:@me -author:@me",
}


def _split_repo(repo: str) -> tuple[str, str]:
    owner, name = repo.split("/", 1)
    return owner, name


def _eprint(*args) -> None:
    print(*args, file=sys.stderr)


def sync(config_dir: str, cache_path: str, now: datetime | None = None) -> list[dict]:
    now = now or datetime.now(timezone.utc)
    cfg = config_mod.load(config_dir)
    cache = Cache(cache_path)
    # Invalidate cached derivations if escalation rules or logic version changed.
    fingerprint = hashlib.sha256(
        (json.dumps(cfg.escalation, sort_keys=True) + derive.DERIVE_VERSION).encode()
    ).hexdigest()
    cache.ensure_fingerprint(fingerprint)
    clients: dict[str, GitHubClient] = {}
    viewers: dict[str, str] = {}
    records: list[dict] = []

    for repo in cfg.active_repos():
        owner, name = _split_repo(repo)
        token = cfg.token_for(owner)
        if not token:
            _eprint(f"[skip] {repo}: ${cfg.env_var_for(owner)} not set")
            continue
        if owner not in clients:
            try:
                clients[owner] = GitHubClient(token)
                viewers[owner] = clients[owner].viewer_login()
            except GitHubError as exc:
                _eprint(f"[error] {repo}: auth/viewer failed: {exc}")
                continue
        client = clients[owner]
        viewer = viewers[owner]

        # Light pass: which PRs are relevant, and which changed since last sync.
        light: dict[int, dict] = {}
        requested_numbers: set[int] = set()
        try:
            for category, qual in _CATEGORIES.items():
                hits = client.search_light(f"repo:{repo} is:open is:pr {qual}")
                for h in hits:
                    light[h["number"]] = h
                    if category == "requested":
                        requested_numbers.add(h["number"])
        except GitHubError as exc:
            _eprint(f"[error] {repo}: search failed: {exc}")
            continue

        for number, light_row in light.items():
            requested = number in requested_numbers
            cached = cache.get(repo, number)
            if cached and cached["updated_at"] == light_row["updatedAt"]:
                record = cached["record"]
                # `requested` can flip without updatedAt changing; refresh it.
                record["relevance"]["requested"] = requested
            else:
                try:
                    detail = client.fetch_detail(owner, name, number)
                except GitHubError as exc:
                    _eprint(f"[error] {repo}#{number}: detail failed: {exc}")
                    continue
                record = derive.build_record(
                    detail, viewer, requested, cfg.escalation, now=now
                )
                if record is not None:
                    record["repo"] = repo
                    cache.put(repo, number, light_row["updatedAt"],
                              light_row["headRefOid"], record)
            if record is not None:
                records.append(record)

    cache.close()
    return records


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Sync review-relevant PRs to PRRecord JSON.")
    here = os.path.dirname(os.path.abspath(__file__))
    repo_root = os.path.abspath(os.path.join(here, "..", "..", "..", ".."))
    parser.add_argument("--config-dir", default=os.path.join(repo_root, "config"))
    parser.add_argument("--cache", default=os.path.join(repo_root, "state", "pr_cache.sqlite"))
    parser.add_argument("--out", default="-", help="output path, or - for stdout")
    args = parser.parse_args(argv)

    os.makedirs(os.path.dirname(args.cache), exist_ok=True)
    records = sync(args.config_dir, args.cache)
    payload = json.dumps(records, indent=2)
    if args.out == "-":
        print(payload)
    else:
        with open(args.out, "w", encoding="utf-8") as fh:
            fh.write(payload)
        _eprint(f"[ok] wrote {len(records)} records to {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
