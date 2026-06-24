---
name: assemble-rounds
description: "The nightly orchestrator. Syncs the queue, proposes the self-requested triage panel, reconciles last cycle's drafts against what you actually posted, fans out per-PR workups to resident subagents, and renders one unified rounds view — fresh / re-review / housekeeping — with DRAFT dispositions only. Zero GitHub writes; you co-sign each write in the morning. Use to assemble a daily round, or as the body of the remote routine."
---

# assemble-rounds

The conductor. Every other skill is a section; this one runs the whole round.
It is the body of the remote routine (see `routine-prompt.md`,
`ROUTINE_SETUP.md`) and is equally runnable locally.

**Hard invariant: zero GitHub writes.** Everything produced is a DRAFT for the
attending to co-sign in the morning session (§5, §6). No `gh pr edit`, no review
submit, no comment post, no merge — ever, in any subagent. The token is
read-only by posture; this skill must not rely on that to stay honest.

All state goes through the **Store seam** (`store.py`): a `cache/` namespace
(GitHub-derived, prunable) and a `ledger/` namespace (drafts + the §5 learning
series, durable). Never `open("state/...")` by hand — the seam is what lets the
backend swap to LanceDB later, and the `claude/state` round-trip (Steps 0 / 7)
is what makes any of this survive a remote run.

## Step 0 — hydrate state (durability keystone)

```sh
python3 .claude/skills/assemble-rounds/scripts/state_sync.py hydrate
```

A remote run is a fresh clone — `state/` starts empty. This fetches the
`claude/state` branch and unpacks last run's cache + ledger so reconciliation has
a prior cycle, the relevance profile is warm, and the workup cache can hit.
First run / no branch: it starts empty and says so. (Local runs: harmless no-op
if the branch doesn't exist.)

## Step 1 — sync the queue (deterministic)

```sh
set -a; source .env; set +a
python3 .claude/skills/pr-sync/scripts/sync.py --out state/cache/records.json
```

Colleague-requested + already-reviewed PRs, lane-assigned. No LLM, owns the
correctness traps. Reuses the warm `cache/pr_detail.sqlite` to skip detail
fetches for unchanged PRs.

## Step 2 — propose the self-requested panel (deterministic)

```sh
python3 .claude/skills/pr-relevance/scripts/relevance.py --top 10 --out state/cache/panel.json
```

## Step 3 — reconcile last cycle (the learning loop, §5)

```sh
python3 .claude/skills/assemble-rounds/scripts/reconcile.py
```

Folds the previous cycle's drafted dispositions (`ledger/dispositions/*`) against
the reviews you actually posted, into per-domain agreement / over-call /
under-call / anchoring logs at `ledger/reconcile/agreement.json`. **Log from day
one even though we don't act on it yet.** Read-only; must run before you write
this cycle's dispositions.

## Step 4 — fan out per-PR workups to resident subagents

For each PR needing a deep look, **consult the workup cache first** — a SOAP is
the expensive part (~15–35K tokens), and it is still valid if the head SHA has
not moved (a force-push is exactly what invalidates it, §3):

```sh
# headRefOid comes from the PRRecord (state/cache/records.json).
python3 .claude/skills/assemble-rounds/scripts/workup.py get \
    --repo owner/name --number 7416 --sha <headRefOid>
```

- **exit 0** → it prints the cached SOAP. Reuse it; **spawn no subagent.**
- **exit 3 (miss)** → spawn the resident subagent in **isolated context**, then
  cache its SOAP for next time:

```sh
echo "<SOAP text>" | python3 .claude/skills/assemble-rounds/scripts/workup.py put \
    --repo owner/name --number 7416 --sha <headRefOid> --at <cycle-iso>
```

Route by lane (wrap per repo in try/continue — **a failed repo or PR is
reported, never aborts the round**):

- **fresh lane** (and any triage candidate the attending has pre-confirmed) →
  the `fresh-review` skill (builds its own packet, produces a SOAP review).
- **re_review lane** → the `re-review-delta` skill (conditions ledger + scoped
  delta). A changed PR has a new head SHA, so its workup naturally cache-misses.
- **housekeeping** → no deep workup. It's discharge planning: surface
  *approved-not-merged* and *stale-waiting-on-author* with the two columns
  render already shows. Drafting a nudge is fine; posting it is not.

## Step 5 — persist this cycle's DRAFT dispositions

Write the cycle to `state/ledger/dispositions/<cycle>.json` (sanitize `:` in the
filename) so the *next* run's reconcile can diff it against what you posted. One
entry per PR you formed a disposition on:

```json
{
  "cycle": "2026-06-23T06:00:00Z",
  "viewer": "<your login>",
  "dispositions": [
    {"repo": "owner/name", "number": 7416, "lane": "fresh",
     "domain": "rust/lance-index",
     "recommendation": "approve",            // approve | block | comment
     "drafted_blocking_count": 0,
     "drafted_at": "2026-06-23T06:00:00Z"}
  ]
}
```

`domain` is the PR's primary area (its top changed crate / dir — the same bucket
`pr-relevance` uses); it is the axis §5 tracks agreement along. `recommendation`
is the SOAP RECOMMENDATION call. Use UTC timestamps; pass them in, don't invent
precision you don't have. This is **ledger** — written once per cycle, never
pruned.

## Step 6 — render the unified round

```sh
python3 .claude/skills/assemble-rounds/scripts/assemble.py
```

prints the deterministic frame: reconciliation banner → triage panel → three
lanes. Present it, then attach each PR's SOAP workup from Step 4 under its lane
entry. Keep the order render gives (fresh = acuity; re-review = proximity to
merge; housekeeping batched). Residents are **queryable, not one-shot** (§3): if
the attending probes ("did you check the null case?"), rewrite the disposition
and its draft, don't just answer.

## Step 7 — persist state (durability keystone)

```sh
python3 .claude/skills/assemble-rounds/scripts/state_sync.py persist \
    --message "cycle <cycle-iso>"
```

Prunes dead `cache/` workups (PRs no longer in the queue), commits the whole
`state/` tree as the new `claude/state` head, and pushes. `ledger/` is never
pruned. The push rides the clone's git credentials, **not** the read-only API
PAT (§6). Local-only run: add `--no-push` to keep it on a local branch.

## Done / hand-off

The night run ends here: rounds assembled, dispositions persisted, state pushed,
**nothing written to GitHub's review surface**. The morning session reads the
three lanes and co-signs each write individually — that co-sign, under the
attending's own identity, is the accountability model (§6, §9).
