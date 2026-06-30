---
name: re-review-delta
description: "Re-review an assigned PR that has changed since you last looked. Reconstructs your prior conditions ledger from posted issue(blocking) comments and verifies each met/not-met/moot against the actual diff (NOT GitHub's resolved flag), plus a fresh-eyes pass over everything that changed since your last review. Use for PRs in the re_review lane, or when asked to re-review / check if feedback was addressed."
---

# re-review-delta

The **consuming** half of the conditions model. For a PR in the `re_review`
lane (you reviewed it, the author pushed since), produce two sections that
**never collapse into one** (plan §3).

Read-only. The output is a draft re-review for the attending to co-sign.

## Step 1 — build the packet (deterministic)

```sh
set -a; source .env; set +a
# PR-scoped --out: slug with slashes as dashes, e.g. packet-lancedb-lancedb-3303.json.
python3 .claude/skills/re-review-delta/scripts/rereview.py OWNER/REPO NUMBER \
    --out state/cache/packet-OWNER-REPO-NUMBER.json
```

**Never write a shared `state/packet.json`.** Under `assemble-rounds` many
residents build packets in parallel; a fixed path races — one PR's packet
clobbers another's. The per-PR `--out` above is collision-free (`state/cache/`
already exists post-sync, so no `mkdir`).

The packet has: `pr` (with `last_reviewed_sha` → `head`), `merge_state`
(CI/mergeability fetched **live at build time** — trust it over the synced
record, don't re-fetch CI by hand), `conditions` (your reconstructed ledger,
each `status: open`), and `delta` (the commit-anchored
diff `last_reviewed_sha...head` — trap #2, NOT GitHub "changes since"). The
delta is **scoped to the PR's own changed files**: a long-lived branch that
merged its base in would otherwise swamp the diff with the base branch's churn.
`delta.files_off_branch_excluded` reports how many such files were dropped.

## Step 2 — Section A: closed-ended conditions ledger

For **each** condition in the packet, assign a status by **verifying against the
delta patch**, not the author's claim:

- **`author_resolved: true` is a CLAIM, not a fact (trap #3).** A resolved
  thread only means the author clicked resolve. Confirm in the diff.
- **met** — the diff actually does what the condition asked. Cite the new line /
  hunk as `evidence_ref`.
- **not_met** — still outstanding (even if resolved). Cite what's missing.
- **moot** — the author took a different approach that makes the condition no
  longer apply (common and real). Say what approach replaced it.
- **open** — the changed region isn't in this delta; you can't yet confirm.

If a condition's file/line is **not** in the delta, the author hasn't touched it
since — it cannot be `met` by new work; it stays `not_met`/`open`.

## Step 3 — Section B: open-ended fresh-eyes delta

Independently read the delta patches. Ask: **did anything that changed since I
last looked introduce something new and bad?** Conditions tell you where you
already looked; regressions hide where you didn't. A fix to condition #3 can
break something that was never on the list. Surface new findings as draft
`issue(...)` comments (see `conventional-comments`).

## Step 4 — present (draft, never post)

```
RE-REVIEW · {repo}#{number} — {title}
reviewed {last_reviewed_sha[:8]} → {head[:8]}  ({N} commits, {M} files)

CONDITIONS LEDGER ({met}/{total} met)
  ✓ met      [blocking] {subject}
             → {evidence: file:line / hunk}
  ✗ not_met  [blocking] {subject}   (author marked resolved — NOT confirmed)
             → {what is still missing}
  ~ moot     [blocking] {subject}
             → {the different approach taken}

FRESH-EYES DELTA
  {new findings in code changed since last review, or "no new concerns"}

RECOMMENDATION: {approve / block on N conditions / needs a sitting}
  confidence: {…} · what would make this wrong: {…}
```

Keep the two sections visually separate. Verify against ground truth (the
patch), not your own prose. Every status needs a diff-anchored evidence line; an
unsupported "met" must be conspicuous.
