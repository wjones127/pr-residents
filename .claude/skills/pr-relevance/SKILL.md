---
name: pr-relevance
description: "Propose the self-requested half of the triage panel: open PRs nobody routed to you that look like your work, each with a rationale. Ranks by per-repo path-affinity learned from the PRs you've reviewed, with a cold-start fallback (declared interests + escalation paths) until that history accumulates. Read-only — proposes candidates for you to confirm/strike; never assigns. Use to find PRs worth reviewing that you weren't asked to."
---

# pr-relevance

Powers the **self-requested** half of triage (§3): colleague-requested PRs are
the explicit half (`pr-sync`'s `requested` category); this skill proposes the
other half — *"these look like yours, here's why"* — for you to confirm or
strike. It does **not** assign or route anything.

Per-user by construction: the affinity profile is built from *your* review
history and cached under `state/` (must-not-share, §9).

## Step 1 — produce the panel (deterministic)

```sh
set -a; source .env; set +a
python3 .claude/skills/pr-relevance/scripts/relevance.py --top 10 --out state/cache/panel.json
```

First run builds your review-history profile from the API (one-time, cached via
the Store seam to `state/cache/relevance_profile.json`). Pass `--rebuild` to refresh it after you've
reviewed more PRs. Knobs: `--history-limit` (PRs sampled for the profile),
`--candidate-limit` (open PRs scored per repo), `--min-score`, `--top`.

Each panel entry: `{repo, number, title, url, author, score, mode, rationale,
matched_areas, relevance:{score, requested:false}}`.

## Step 2 — how candidates are scored

- **`mode: affinity`** (the default, once you have ≥ 5 reviewed PRs in a repo):
  the candidate's changed files are bucketed into areas (crate / top dir) and
  scored by how often you've reviewed each area. `rationale` names the top
  overlapping areas with counts. This is the per-repo, no-ML path-overlap half
  of §8.
- **`mode: cold_start`** (thin history): ranks on the *shared* signals a new
  teammate already has — your declared `interests` (config/user.yml) and
  hard-escalation paths (config/escalation.yml). The rationale says so plainly.
- Files under `exclude_paths` (config/repos.yml) never count, for the profile or
  a candidate.

## Step 3 — present for confirm/strike (proposal, never assign)

Show the ranked panel as a short table — number, title, author, score, and the
one-line rationale. Frame it as a proposal: *"these look like yours — confirm or
strike."* The score orders attention; the rationale is why. Keep it tight.

For each PR the attending **confirms**, hand it to the fresh-lane resident:

```sh
python3 .claude/skills/fresh-review/scripts/freshreview.py OWNER/REPO NUMBER --out state/packet.json
```

then follow `fresh-review`'s SKILL.md to produce the SOAP review. Struck
candidates are simply dropped (capturing strikes as a learning signal is a
graduated-autonomy concern, slice 5 — not done here).

## Honesty / scope

- The score is a **relevance** hint, not an acuity or importance score — a PR
  can be highly relevant to you and low-risk, or irrelevant and critical. Don't
  conflate it with the `acuity` axis.
- Relevance never auto-surfaces a PR into rounds; it only *proposes* it into the
  triage panel. The attending decides.

## Extension point — semantic k-NN (not yet implemented)

§8's second half: embed each reviewed PR's **title + description** (NOT the raw
diff) and k-NN a candidate's embedding against that history — a *global*
semantic signal that complements the *per-repo* path-overlap above (hybrid:
combine the two scores). It switches on once history accumulates and needs an
embedding API key (`config/user.yml: env.embedding_api_key`) and a vector store
(LanceDB) under `state/`. Deferred: the path-overlap half delivers the
high-value result first, and the remote sandbox is stdlib-only (no pip), so the
vector half lands when that constraint is addressed. `affinity.py` exposes the
scoring seam; a `semantic.py` sibling would add the k-NN score and a blender.
