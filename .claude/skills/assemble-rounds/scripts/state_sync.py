"""state_sync: the durability keystone for remote runs.

A remote routine run is a *fresh clone* — `state/` starts empty. Without a
round-trip, reconciliation has no prior cycle to diff, the relevance profile
rebuilds from the API every night, and the workup cache never hits. So this
script round-trips the whole `state/` tree through a dedicated `claude/state`
branch:

  hydrate   (run start) fetch claude/state and unpack it into state/
  persist   (run end)   prune cache/*, commit state/ as the branch's new tree,
                        push. ledger/* is never pruned.

Credentials note: this uses the plain `git` CLI, so the push rides the clone's
own git credentials — NOT the read-only `GITHUB_TOKEN_<ORG>` PATs, which are for
the GitHub data-plane API only (github.py). The two credential paths are
deliberately separate (§6).

The branch tree holds state at its ROOT (cache/…, ledger/… top-level), so it is
a clean, self-contained snapshot independent of main's checkout. We never touch
HEAD or the main working tree — all writes go through a temp index.

Usage:
    state_sync.py hydrate [--branch claude/state] [--remote origin]
    state_sync.py persist --message "cycle 2026-06-23" [--no-push] [--no-prune]
"""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
import tempfile

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(_HERE, "..", "..", "pr-sync", "scripts"))

import store as store_mod  # noqa: E402

DEFAULT_BRANCH = "claude/state"
DEFAULT_REMOTE = "origin"
_IDENTITY = {
    "GIT_AUTHOR_NAME": "pr-residents-routine",
    "GIT_AUTHOR_EMAIL": "pr-residents@localhost",
    "GIT_COMMITTER_NAME": "pr-residents-routine",
    "GIT_COMMITTER_EMAIL": "pr-residents@localhost",
}


def _eprint(*args) -> None:
    print(*args, file=sys.stderr)


def _git(repo: str, args: list[str], *, env: dict | None = None,
         check: bool = True, cwd: str | None = None) -> subprocess.CompletedProcess:
    full = dict(os.environ)
    if env:
        full.update(env)
    return subprocess.run(
        ["git", "--git-dir", os.path.join(repo, ".git"), *args],
        cwd=cwd or repo, env=full, check=check,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    )


# --- pure prune policy -----------------------------------------------------

def _parse_workup_key(key: str) -> tuple[str, int] | None:
    """cache/workups/<owner>/<name>/<number>/<sha>.json -> ('owner/name', number)."""
    parts = key.split("/")
    if len(parts) != 6 or parts[0] != "cache" or parts[1] != "workups":
        return None
    try:
        return f"{parts[2]}/{parts[3]}", int(parts[4])
    except ValueError:
        return None


def workups_to_prune(workup_keys: list[str], active: set[tuple[str, int]]) -> list[str]:
    """Workup-cache keys whose PR is no longer in the active queue (closed /
    merged / dropped). Those PRs won't be reviewed again, so their cached SOAPs
    are dead weight. Keep every SHA of a still-active PR."""
    out = []
    for key in workup_keys:
        parsed = _parse_workup_key(key)
        if parsed is None:
            continue
        if parsed not in active:
            out.append(key)
    return out


def _active_prs(store: store_mod.FileStore) -> set[tuple[str, int]]:
    active: set[tuple[str, int]] = set()
    for art in (store_mod.RECORDS, store_mod.PANEL):
        blob = store.get_json(art) or []
        for row in blob:
            repo, number = row.get("repo"), row.get("number")
            if repo and isinstance(number, int):
                active.add((repo, number))
    return active


def prune_cache(store: store_mod.FileStore) -> int:
    """Drop workup SOAPs for PRs no longer active. cache/ only; ledger/ untouched."""
    active = _active_prs(store)
    if not active:
        return 0  # nothing to anchor on — don't prune blindly
    stale = workups_to_prune(store.list_keys("cache/workups"), active)
    for key in stale:
        store.delete(key)
    return len(stale)


# --- git round-trip --------------------------------------------------------

def hydrate(repo: str, state_dir: str, remote: str = DEFAULT_REMOTE,
            branch: str = DEFAULT_BRANCH) -> bool:
    """Fetch the state branch and unpack it into state_dir. Returns True if a
    snapshot was restored, False on first run (branch absent)."""
    fetched = _git(repo, ["fetch", remote, branch], check=False)
    if fetched.returncode != 0:
        _eprint(f"[state-sync] no {branch} on {remote} yet — starting empty")
        return False
    os.makedirs(state_dir, exist_ok=True)
    # Stream the branch tree out as tar and extract into state_dir, so a binary
    # blob (the SQLite cache) is never round-tripped through text mode.
    tar = subprocess.Popen(
        ["git", "--git-dir", os.path.join(repo, ".git"), "archive", "--format=tar", "FETCH_HEAD"],
        cwd=repo, stdout=subprocess.PIPE,
    )
    subprocess.run(["tar", "-x", "-C", state_dir], stdin=tar.stdout, check=True)
    tar.stdout.close()
    tar.wait()
    _eprint(f"[state-sync] hydrated state/ from {remote}/{branch}")
    return True


def persist(repo: str, state_dir: str, message: str, remote: str = DEFAULT_REMOTE,
            branch: str = DEFAULT_BRANCH, push: bool = True) -> dict:
    """Commit the current state_dir tree as the new head of `branch` and push.

    Uses a temp index + explicit work-tree, so HEAD and the main checkout are
    never touched, and gitignore (state/ is ignored on main) is bypassed via -f.
    """
    if not os.path.isdir(state_dir):
        return {"committed": False, "note": "no state dir"}

    parent = _git(repo, ["fetch", remote, branch], check=False)
    parent_sha = None
    if parent.returncode == 0:
        rev = _git(repo, ["rev-parse", "--verify", "--quiet", "FETCH_HEAD"], check=False)
        parent_sha = rev.stdout.strip() or None

    # The index path must NOT pre-exist: git treats a 0-byte file as a corrupt
    # index. Use a fresh temp dir and let git create the index inside it.
    index_dir = tempfile.mkdtemp(suffix=".state-sync")
    try:
        env = dict(_IDENTITY)
        env["GIT_INDEX_FILE"] = os.path.join(index_dir, "index")
        # Stage everything under state_dir as root-relative (cache/…, ledger/…).
        _git(repo, ["--work-tree", state_dir, "add", "-f", "-A", "."],
             env=env, cwd=state_dir)
        tree = _git(repo, ["write-tree"], env=env).stdout.strip()

        commit_args = ["commit-tree", tree, "-m", message]
        if parent_sha:
            commit_args += ["-p", parent_sha]
        commit = _git(repo, commit_args, env=env).stdout.strip()

        if push:
            _git(repo, ["push", remote, f"{commit}:refs/heads/{branch}"], env=env)
        else:
            _git(repo, ["update-ref", f"refs/heads/{branch}", commit], env=env)
    finally:
        import shutil
        shutil.rmtree(index_dir, ignore_errors=True)

    return {"committed": True, "commit": commit, "tree": tree,
            "parent": parent_sha, "pushed": push}


def _repo_root() -> str:
    return os.path.abspath(os.path.join(_HERE, "..", "..", "..", ".."))


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Round-trip state/ through the claude/state branch.")
    parser.add_argument("op", choices=["hydrate", "persist"])
    parser.add_argument("--repo", default=None, help="repo root (default: this checkout)")
    parser.add_argument("--state-dir", default=None)
    parser.add_argument("--remote", default=DEFAULT_REMOTE)
    parser.add_argument("--branch", default=DEFAULT_BRANCH)
    parser.add_argument("--message", default="pr-residents state")
    parser.add_argument("--no-push", action="store_true")
    parser.add_argument("--no-prune", action="store_true")
    args = parser.parse_args(argv)

    repo = args.repo or _repo_root()
    state_dir = args.state_dir or os.path.join(repo, "state")

    if args.op == "hydrate":
        hydrate(repo, state_dir, args.remote, args.branch)
        return 0

    store = store_mod.FileStore(state_dir)
    if not args.no_prune:
        pruned = prune_cache(store)
        if pruned:
            _eprint(f"[state-sync] pruned {pruned} stale workup(s)")
    result = persist(repo, state_dir, args.message, args.remote, args.branch,
                     push=not args.no_push)
    _eprint(f"[state-sync] {result}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
