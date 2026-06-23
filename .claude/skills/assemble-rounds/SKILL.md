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

## Step 1 — sync the queue (deterministic)

```sh
set -a; source .env; set +a
python3 .claude/skills/pr-sync/scripts/sync.py --out state/records.json
```

Colleague-requested + already-reviewed PRs, lane-assigned. No LLM, owns the
correctness traps.

## Step 2 — propose the self-requested panel (deterministic)

```sh
python3 .claude/skills/pr-relevance/scripts/relevance.py --top 10 --out state/panel.json
```

## Step 3 — reconcile last cycle (the learning loop, §5)

```sh
python3 .claude/skills/assemble-rounds/scripts/reconcile.py
```

Folds the previous cycle's drafted dispositions against the reviews you actually
posted, into per-domain agreement / over-call / under-call / anchoring logs under
`state/reconcile/`. **Log from day one even though we don't act on it yet.** This
is read-only and must run before you overwrite this cycle's dispositions.

## Step 4 — fan out per-PR workups to resident subagents

Spawn one subagent per PR that needs a deep look, each in **isolated context**.
Wrap per repo in try/continue: **a failed repo or PR is reported, never aborts
the round.** Collect what succeeds; list what didn't.

- **fresh lane** (and any triage candidate the attending has pre-confirmed) →
  the `fresh-review` skill (builds its own packet, produces a SOAP review).
- **re_review lane** → the `re-review-delta` skill (conditions ledger + scoped
  delta).
- **housekeeping** → no deep workup. It's discharge planning: surface
  *approved-not-merged* and *stale-waiting-on-author* with the two columns
  render already shows. Drafting a nudge is fine; posting it is not.

Re-validate at round time: a workup built hours ago is stale after a force-push
(§3). If `headRefOid` moved since the packet was built, rebuild it.

## Step 5 — persist this cycle's DRAFT dispositions

Write `state/dispositions/<cycle>.json` so the *next* run's reconcile can diff it
against what you posted. One entry per PR you formed a disposition on:

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
precision you don't have.

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

## Done / hand-off

The night run ends here: rounds assembled, dispositions persisted, **nothing
written to GitHub**. The morning session reads the three lanes and co-signs each
write individually — that co-sign, under the attending's own identity, is the
accountability model (§6, §9).
