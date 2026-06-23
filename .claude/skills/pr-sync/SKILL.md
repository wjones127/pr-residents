---
name: pr-sync
description: "Fetch review-relevant PRs across the configured repos and produce the daily triaged, three-lane rounds list (fresh / re-review / housekeeping). Use when asked to sync PRs, build the review queue, show what needs reviewing today, or refresh PR triage. Deterministic, no LLM, no GitHub writes."
---

# pr-sync

Producer of the canonical `PRRecord` (see `docs/prrecord.md`) and the daily
three-lane rounds list. Deterministic, read-only, stdlib-only Python — no LLM,
no GitHub writes. Replaces the legacy `fetch-review-prs.sh` + `triage-prs`.

## What it does

1. `sync.py` — per configured repo, runs two GraphQL searches (`review-requested:@me`,
   `reviewed-by:@me`), derives `blocked_on` / `lane` / `acuity` / `effort` /
   `merge_state` / `escalation` for each PR, and emits `PRRecord` JSON. SQLite
   cache (`state/pr_cache.sqlite`) skips the heavy detail fetch for PRs whose
   `updatedAt` is unchanged; the cache auto-invalidates when `escalation.yml` or
   the derivation logic changes.
2. `render.py` — prints the three lanes: **fresh** (acuity-ordered), **re-review**
   (proximity-to-merge ordered), **housekeeping** (approved-not-merged +
   stale-waiting-on-author, batched).

## Running it

Tokens are per-org, read-only, in env (`GITHUB_TOKEN_<ORG>`, e.g.
`GITHUB_TOKEN_LANCE_FORMAT`). Locally, load `.env` first:

```sh
set -a; source .env; set +a
python3 .claude/skills/pr-sync/scripts/sync.py | python3 .claude/skills/pr-sync/scripts/render.py
```

Or persist the JSON for downstream skills:

```sh
python3 .claude/skills/pr-sync/scripts/sync.py --out state/records.json
python3 .claude/skills/pr-sync/scripts/render.py state/records.json
```

Flags: `--config-dir` (default `config/`), `--cache` (default
`state/pr_cache.sqlite`), `--out` (`-` for stdout). A repo whose org token is
missing is skipped with a stderr note, not an error.

## Correctness traps owned here (see docs/prrecord.md)

- `pushedDate` is dead (returns null) — "author acted since I looked" uses SHA
  identity (`headRefOid` ≠ `last_reviewed_sha`) + `HeadRefForcePushedEvent`.
- Re-review delta anchors on the commit I last reviewed, not GitHub "changes since."
- A late push under a stale approval surfaces back to me (`blocked_on=me`).
- No auto-merge: approved-not-merged is `blocked_on=merge` → housekeeping.

## Not yet (later slices)

`conditions` is emitted empty — reconstructed from `issue(blocking)` history in
slice 2 (`re-review-delta`). `relevance.score` is null until slice 4
(`pr-relevance`); `relevance.requested` is populated now.

## Tests

```sh
python3 .claude/skills/pr-sync/tests/test_derive.py
```
