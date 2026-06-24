"""pr-relevance: rank open PRs nobody asked me to review by how much they look
like *my* work, for the self-requested half of the triage panel (§3, §8).

Deterministic, no LLM. Path-affinity (per-repo) learned from the PRs I have
reviewed; cold-start fallback (declared interests + hard-escalation paths) when
that history is too thin. The semantic k-NN half is a documented extension point
(see SKILL.md) — not implemented here.

Per-user by construction: the profile is built from *my* review history and
cached under state/ (must-not-share, §9).

Usage:
    python3 relevance.py [--config-dir DIR] [--state-dir DIR] [--rebuild]
                         [--history-limit N] [--candidate-limit N]
                         [--top K] [--min-score F] [--out PATH]
"""

from __future__ import annotations

import argparse
import json
import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(_HERE, "..", "..", "pr-sync", "scripts"))

import config as config_mod  # noqa: E402
import derive  # noqa: E402
import store as store_mod  # noqa: E402
from github import GitHubClient, GitHubError  # noqa: E402

import affinity  # noqa: E402
import embed  # noqa: E402

# How hard the semantic cosine (≈0–1) pushes on the blended score. Affinity
# scores are integer area-overlap sums and cold-start scores are tiny, so a
# weight of ~5 lets semantics reorder weak-lexical-signal candidates (its whole
# point — that's where path-affinity is blind) while only nudging strong ones.
DEFAULT_SEMANTIC_WEIGHT = 5.0

# Search nodes carrying the changed paths we score on. `first` is small because
# requesting files inflates GraphQL query cost.
_SEARCH_FILES_QUERY = """
query($q: String!, $cursor: String) {
  search(query: $q, type: ISSUE, first: 25, after: $cursor) {
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest {
        number
        title
        url
        author { login }
        repository { nameWithOwner }
        files(first: 100) { nodes { path } }
      }
    }
  }
}
"""

def _eprint(*args) -> None:
    print(*args, file=sys.stderr)


def _search_prs_with_files(client: GitHubClient, query: str, limit: int) -> list[dict]:
    """Return [{number, title, url, author, repo, paths}] up to `limit`."""
    out: list[dict] = []
    cursor: str | None = None
    while len(out) < limit:
        data = client.graphql(_SEARCH_FILES_QUERY, {"q": query, "cursor": cursor})
        search = data["search"]
        for node in search["nodes"]:
            if not node:
                continue
            out.append({
                "number": node["number"],
                "title": node["title"],
                "url": node["url"],
                "author": (node.get("author") or {}).get("login"),
                "repo": node["repository"]["nameWithOwner"],
                "paths": [f["path"] for f in (node.get("files") or {}).get("nodes") or []],
            })
            if len(out) >= limit:
                break
        page = search["pageInfo"]
        if not page["hasNextPage"]:
            break
        cursor = page["endCursor"]
    return out


def _filtered(paths: list[str], exclude: list[str]) -> list[str]:
    if not exclude:
        return paths
    return [p for p in paths if not any(derive.path_matches(p, pat) for pat in exclude)]


def build_profiles(client: GitHubClient, repos: list[str], exclude: list[str],
                   history_limit: int) -> dict[str, dict]:
    """Per-repo path-affinity profile from my reviewed-PR history."""
    profiles: dict[str, dict] = {}
    for repo in repos:
        reviewed = _search_prs_with_files(
            client, f"repo:{repo} is:pr reviewed-by:@me -author:@me", history_limit)
        paths_per_pr = [_filtered(pr["paths"], exclude) for pr in reviewed]
        profiles[repo] = {
            "reviews": len(reviewed),
            "weights": affinity.build_profile(paths_per_pr),
        }
    return profiles


def _load_profiles(store, viewer: str) -> dict[str, dict] | None:
    blob = store.get_json(store_mod.RELEVANCE_PROFILE)
    if not blob:
        return None
    if blob.get("viewer") != viewer:
        return None  # never reuse another identity's profile (§9)
    return blob.get("profiles")


def _save_profiles(store, viewer: str, profiles: dict[str, dict]) -> None:
    store.put_json(store_mod.RELEVANCE_PROFILE, {"viewer": viewer, "profiles": profiles})


def _score_repo(candidates: list[dict], repo_profile: dict, cfg, *,
                store=None, semantic: bool = False,
                semantic_weight: float = DEFAULT_SEMANTIC_WEIGHT,
                model: str = embed.DEFAULT_MODEL, host: str = embed.DEFAULT_HOST) -> list[dict]:
    weights = repo_profile["weights"]
    reviews = repo_profile["reviews"]
    centroid = repo_profile.get("centroid")
    cold = reviews < affinity.MIN_HISTORY
    panel: list[dict] = []
    for c in candidates:
        paths = _filtered(c["paths"], cfg.exclude_paths)
        if cold:
            esc = derive.match_escalation(paths, [], 0, cfg.escalation)
            base, rationale = affinity.cold_start_score(
                paths, cfg.interests, esc.get("rule_ids"))
            mode, matched = "cold_start", []
        else:
            base, matched = affinity.score_candidate(paths, weights)
            rationale = affinity.affinity_rationale(matched)
            mode = "affinity"
        sem = 0.0
        if semantic and centroid and store is not None:
            vec = embed.embed_cached(store, c["title"], model=model, host=host,
                                     prefix=_task_prefix(model, "query"))
            if vec is not None:
                sem = embed.cosine(vec, centroid)
                rationale += f" · semantic {sem:.2f}"
        score = base + semantic_weight * sem
        panel.append({
            "repo": c["repo"], "number": c["number"], "title": c["title"],
            "url": c["url"], "author": c["author"],
            "score": round(score, 2), "base_score": round(base, 2),
            "semantic": round(sem, 3), "mode": mode, "rationale": rationale,
            "matched_areas": [b for b, _ in matched],
            "files_changed_count": len(paths),
            "relevance": {"score": round(score, 2), "requested": False},
        })
    return panel


def _task_prefix(model: str, kind: str) -> str:
    """nomic-embed-text is an asymmetric retrieval model — it expects a task
    instruction prefix, and reviewed history (the corpus) vs a candidate (the
    query) get different ones, which spreads otherwise-bunched cosines. Harmless
    to skip for models that don't use prefixes."""
    if "nomic" in model.lower():
        return "search_document: " if kind == "document" else "search_query: "
    return ""


def _semantic_centroid(client: GitHubClient, repo: str, store, exclude: list[str],
                       history_limit: int, model: str, host: str):
    """Centroid of the embeddings of my reviewed-PR titles in this repo — the
    semantic analog of the path-affinity profile. None if no history / no daemon."""
    reviewed = _search_prs_with_files(
        client, f"repo:{repo} is:pr reviewed-by:@me -author:@me", history_limit)
    prefix = _task_prefix(model, "document")
    vecs = []
    for pr in reviewed:
        vec = embed.embed_cached(store, pr["title"], model=model, host=host, prefix=prefix)
        if vec is not None:
            vecs.append(vec)
    return embed.centroid(vecs)


def run(config_dir: str, state_dir: str, rebuild: bool, history_limit: int,
        candidate_limit: int, top: int, min_score: float, *,
        semantic: bool = False, semantic_weight: float = DEFAULT_SEMANTIC_WEIGHT,
        embed_model: str = embed.DEFAULT_MODEL, embed_host: str = embed.DEFAULT_HOST
        ) -> list[dict]:
    cfg = config_mod.load(config_dir)
    store = store_mod.FileStore(state_dir)

    # One client per owner (token is per-org). Viewer must be consistent.
    clients: dict[str, GitHubClient] = {}
    viewer: str | None = None
    for repo in cfg.active_repos():
        owner = repo.split("/", 1)[0]
        if owner in clients:
            continue
        token = cfg.token_for(owner)
        if not token:
            _eprint(f"[skip] {repo}: ${cfg.env_var_for(owner)} not set")
            continue
        try:
            clients[owner] = GitHubClient(token)
            viewer = viewer or clients[owner].viewer_login()
        except GitHubError as exc:
            _eprint(f"[error] {repo}: auth/viewer failed: {exc}")
    if viewer is None:
        return []

    repos = [r for r in cfg.active_repos() if r.split("/", 1)[0] in clients]

    profiles = None if rebuild else _load_profiles(store, viewer)
    if profiles is None:
        _eprint("[info] building review-history profile (this hits the API)…")
        profiles = {}
        for repo in repos:
            client = clients[repo.split("/", 1)[0]]
            profiles.update(build_profiles(client, [repo], cfg.exclude_paths, history_limit))
        _save_profiles(store, viewer, profiles)

    # Semantic half (§8 k-NN): opt-in, and only if the local embedder is up.
    # Degrades silently to path-affinity otherwise — never blocks the round.
    if semantic and not embed.available(embed_host):
        _eprint(f"[warn] --semantic set but no embedder at {embed_host}; using path-affinity only")
        semantic = False
    if semantic:
        dirty = False
        for repo in repos:
            rp = profiles.setdefault(repo, {"reviews": 0, "weights": {}})
            if "centroid" not in rp:
                client = clients[repo.split("/", 1)[0]]
                rp["centroid"] = _semantic_centroid(
                    client, repo, store, cfg.exclude_paths, history_limit,
                    embed_model, embed_host)
                dirty = True
        if dirty:
            _save_profiles(store, viewer, profiles)

    panel: list[dict] = []
    for repo in repos:
        client = clients[repo.split("/", 1)[0]]
        repo_profile = profiles.get(repo) or {"reviews": 0, "weights": {}}
        # Candidates: open PRs no one routed to me and I haven't touched.
        q = (f"repo:{repo} is:open is:pr -author:@me "
             f"-review-requested:@me -reviewed-by:@me sort:updated-desc")
        try:
            candidates = _search_prs_with_files(client, q, candidate_limit)
        except GitHubError as exc:
            _eprint(f"[error] {repo}: candidate search failed: {exc}")
            continue
        panel.extend(_score_repo(
            candidates, repo_profile, cfg, store=store, semantic=semantic,
            semantic_weight=semantic_weight, model=embed_model, host=embed_host))

    panel = [p for p in panel if p["score"] >= min_score]
    panel.sort(key=lambda p: (-p["score"], p["repo"], p["number"]))
    return panel[:top]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Rank self-requested triage candidates.")
    repo_root = os.path.abspath(os.path.join(_HERE, "..", "..", "..", ".."))
    parser.add_argument("--config-dir", default=os.path.join(repo_root, "config"))
    parser.add_argument("--state-dir", default=os.path.join(repo_root, "state"))
    parser.add_argument("--rebuild", action="store_true",
                        help="rebuild the review-history profile from the API")
    parser.add_argument("--history-limit", type=int, default=100)
    parser.add_argument("--candidate-limit", type=int, default=50)
    parser.add_argument("--top", type=int, default=10)
    parser.add_argument("--min-score", type=float, default=1.0)
    parser.add_argument("--semantic", action="store_true",
                        help="blend in local-embedding similarity (needs Ollama; §8 k-NN)")
    parser.add_argument("--semantic-weight", type=float, default=DEFAULT_SEMANTIC_WEIGHT)
    parser.add_argument("--embed-model", default=embed.DEFAULT_MODEL)
    parser.add_argument("--embed-host", default=embed.DEFAULT_HOST)
    parser.add_argument("--out", default="-")
    args = parser.parse_args(argv)

    panel = run(args.config_dir, args.state_dir, args.rebuild, args.history_limit,
                args.candidate_limit, args.top, args.min_score,
                semantic=args.semantic, semantic_weight=args.semantic_weight,
                embed_model=args.embed_model, embed_host=args.embed_host)
    payload = json.dumps(panel, indent=2)
    if args.out == "-":
        print(payload)
    else:
        with open(args.out, "w", encoding="utf-8") as fh:
            fh.write(payload)
        _eprint(f"[ok] wrote {len(panel)} relevance candidates to {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
