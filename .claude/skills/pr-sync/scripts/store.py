"""Store: the persistence seam. Skills go through this; they never
`open("state/...")` directly.

Two namespaces live under the state dir, and the split is load-bearing:

  cache/   100% reconstructable from GitHub (PRRecords, the relevance profile,
           the PR-detail SQLite, the workup SOAPs). Losing it costs recompute,
           never correctness — so it is best-effort and prunable.
  ledger/  the drafts and the accumulated draft-vs-posted learning series (§5).
           This exists *nowhere else* — GitHub never saw the drafts, by design —
           so it is durable: written once per cycle, never pruned.

Today the backend is the local filesystem (FileStore); a remote routine wraps
the same `state/` tree with a git round-trip (see state_sync.py) so it survives a
fresh clone. The seam is what lets the backend become LanceDB-on-object-storage
later without touching a single caller.

Keys are POSIX-style and MUST start with `cache/` or `ledger/`. That prefix is
how the backend decides what to prune vs keep.
"""

from __future__ import annotations

import json
import os
from typing import Any

CACHE = "cache"
LEDGER = "ledger"


def default_state_dir() -> str:
    here = os.path.dirname(os.path.abspath(__file__))
    repo_root = os.path.abspath(os.path.join(here, "..", "..", "..", ".."))
    return os.path.join(repo_root, "state")


def _validate_key(key: str) -> None:
    if not key or key.startswith("/") or "\\" in key:
        raise ValueError(f"bad store key: {key!r}")
    head = key.split("/", 1)[0]
    if head not in (CACHE, LEDGER):
        raise ValueError(f"store key must start with cache/ or ledger/: {key!r}")
    if ".." in key.split("/"):
        raise ValueError(f"store key may not contain '..': {key!r}")


class FileStore:
    """Filesystem-backed store rooted at a state dir. The whole tree is what the
    git backend round-trips, so anything written here survives a remote run iff
    state_sync persists it."""

    def __init__(self, root: str | None = None):
        self.root = root or default_state_dir()

    def local_path(self, key: str, *, make_parents: bool = False) -> str:
        """Materialize a real filesystem path for `key`. Needed by callers that
        can't take bytes (e.g. sqlite3.connect)."""
        _validate_key(key)
        path = os.path.join(self.root, *key.split("/"))
        if make_parents:
            os.makedirs(os.path.dirname(path), exist_ok=True)
        return path

    def exists(self, key: str) -> bool:
        return os.path.exists(self.local_path(key))

    def get_json(self, key: str) -> Any | None:
        path = self.local_path(key)
        if not os.path.exists(path):
            return None
        try:
            with open(path, encoding="utf-8") as fh:
                return json.load(fh)
        except (OSError, ValueError):
            return None

    def put_json(self, key: str, obj: Any) -> None:
        path = self.local_path(key, make_parents=True)
        with open(path, "w", encoding="utf-8") as fh:
            json.dump(obj, fh, indent=2)

    def get_text(self, key: str) -> str | None:
        path = self.local_path(key)
        if not os.path.exists(path):
            return None
        try:
            with open(path, encoding="utf-8") as fh:
                return fh.read()
        except OSError:
            return None

    def put_text(self, key: str, text: str) -> None:
        path = self.local_path(key, make_parents=True)
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(text)

    def delete(self, key: str) -> bool:
        path = self.local_path(key)
        if os.path.exists(path):
            os.remove(path)
            return True
        return False

    def list_keys(self, prefix: str) -> list[str]:
        """All file keys under `prefix` (a key prefix, e.g. 'ledger/dispositions'
        or 'cache/workups'), POSIX-style, sorted oldest-name first."""
        _validate_key(prefix.rstrip("/") + "/x")  # prefix must be in a namespace
        base = os.path.join(self.root, *prefix.split("/"))
        if not os.path.isdir(base):
            return []
        out: list[str] = []
        for dirpath, _dirs, files in os.walk(base):
            rel_dir = os.path.relpath(dirpath, self.root)
            for fn in files:
                rel = os.path.join(rel_dir, fn)
                out.append(rel.replace(os.sep, "/"))
        return sorted(out)


# --- artifact key registry -------------------------------------------------
# One place that knows the on-disk layout, so the cache/ledger split and the
# swap to a different backend stay centralized.

RECORDS = "cache/records.json"
PANEL = "cache/panel.json"
RELEVANCE_PROFILE = "cache/relevance_profile.json"
PR_DETAIL_DB = "cache/pr_detail.sqlite"

DISPOSITIONS_PREFIX = "ledger/dispositions"
RECONCILE_AGREEMENT = "ledger/reconcile/agreement.json"


def _safe_segment(s: str) -> str:
    """Make a string safe as a single path segment (slashes, dots collapsed)."""
    return "".join(c if (c.isalnum() or c in "-_.") else "_" for c in s)


def disposition_key(cycle: str) -> str:
    return f"{DISPOSITIONS_PREFIX}/{_safe_segment(cycle)}.json"


def workup_key(repo: str, number: int, sha: str) -> str:
    """The workup cache key: a SOAP is valid only for the exact head SHA it was
    written against (§3 — a force-push invalidates it)."""
    owner, name = (repo.split("/", 1) + [""])[:2]
    return f"cache/workups/{_safe_segment(owner)}/{_safe_segment(name)}/{int(number)}/{_safe_segment(sha)}.json"


def embedding_key(model: str, content_hash: str) -> str:
    """Cache key for an embedding vector. Scoped by model name so swapping the
    embedding model never serves stale vectors from a different one."""
    return f"cache/embeddings/{_safe_segment(model)}/{_safe_segment(content_hash)}.json"
