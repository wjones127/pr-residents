# `PRRecord` — the producer/consumer contract

`pr-sync` (slice 1, deterministic, no LLM) is the **sole producer**. Everything
downstream — renderer, residents, `re-review-delta`, `pr-relevance`,
`assemble-rounds` — **consumes** this and does not re-fetch from GitHub.

Pinning this now (slice 0) is the interface lock: consumers code against these
field semantics, not against GraphQL.

## Shape

```
PRRecord {
  repo              : "owner/name"
  number            : int
  url               : string
  title             : string
  author            : string                              # GitHub login

  blocked_on        : me | author | merge | other_reviewer
  age_in_state_hrs  : number                              # hours since the event that put it in this state
  lane              : fresh | re_review | housekeeping

  acuity            : { risk: low|med|high,
                        urgency: low|med|high,
                        rationale: string }               # WHY this risk, human-readable
  effort            : { size_bucket: XS|S|M|L|XL,
                        additions: int, deletions: int, files: int }

  relevance         : { score: number, requested: bool }  # triage candidates (slice 4); requested known earlier

  last_reviewed_sha : sha | null
  delta             : { commits: [sha...],
                        files_touched: [path...] } | null  # re_review only; null otherwise

  conditions        : [ { id: string,
                          text: string,
                          kind: blocking | non_blocking | suggestion,
                          status: met | not_met | moot | open,
                          evidence_ref: string,            # line ref / test / call site backing the status
                          author_resolved: bool } ]        # did author click "resolve"? (a CLAIM, not status)

  merge_state       : { ci: green|red|pending, mergeable: bool }

  escalation        : { forced: bool, rule_ids: [string], reason: string }  # from config/escalation.yml
}
```

> `escalation` is added beyond the §8 sketch: the bright-line rules (§5) must
> ride on the record so consumers can't accidentally fast-track a forced PR. If
> `escalation.forced`, lane is pinned to `fresh` and `acuity.risk` to `high`
> regardless of any learned signal.

## `blocked_on` derivation (GraphQL timeline, not ML — §2a)

The single highest-leverage derivation. Determine the one party each open PR
waits on:

- **`me`** — review requested from me (directly or via a team I'm in) and I have
  not reviewed the current head, OR I previously reviewed and the author has
  pushed since (re-review owed). Drives `fresh` / `re_review`.
- **`author`** — I (or another reviewer) requested changes / left open blocking
  threads and the author has not pushed a response since. Not surfaced unless
  stale enough for a ping.
- **`merge`** — approved (by me) but not merged: stuck on CI, a required second
  approval, or a human to click merge. Drives `housekeeping`.
- **`other_reviewer`** — review pending from someone else, not me.

`age_in_state_hrs` measures from the event that *entered* the current state (my
review submitted, author's last push, my approval), not from PR creation.

## Lane derivation

| `blocked_on` | condition | `lane` |
|---|---|---|
| `me` | `last_reviewed_sha` null OR `delta` empty | `fresh` |
| `me` | `last_reviewed_sha` set AND `delta` non-empty | `re_review` |
| `merge` | — | `housekeeping` |
| `author` | only if stale enough to ping | `housekeeping` (stale-ping) |
| `other_reviewer` | — | not surfaced |

Forced escalation overrides: `escalation.forced` ⇒ `lane = fresh`.

## Correctness traps `pr-sync` must own (§8)

These are latent in manual practice and are the reason `pr-sync` is the
highest-correctness-risk piece, de-risked first.

1. **Detect "author acted since I looked" via SHA identity + timeline events —
   NOT `pushedDate`.** `Commit.pushedDate` is **confirmed dead**: GitHub's
   GraphQL API returns `null` for it on every commit (verified across both
   target repos, Spike A 2026-06-23). Do not reach for it. Instead:
   - **"Did the author push since my last review?"** → `headRefOid` ≠
     `last_reviewed_sha` (SHA identity; see trap #2). No timestamp needed, and
     it is immune to rebases/force-pushes scrambling `committedDate`.
   - **Detect a rebase/force-push** → `HeadRefForcePushedEvent` timeline nodes
     (`createdAt`, `beforeCommit`/`afterCommit`), which is exactly the case that
     scrambles `committedDate`.
   - **`age_in_state_hrs`** → the `createdAt` of the governing timeline event
     (my last `PullRequestReview`, or the latest commit / force-push after it),
     ordered by the timeline — not by `committedDate`, which a rebase rewrites.

   `committedDate` is acceptable only as an approximate *display* timestamp for
   an ordinary (non-force) push; correctness never depends on it.
2. **Delta anchors on `last_reviewed_sha`**, the specific commit I last
   reviewed — **not** GitHub's built-in "changes since" (which anchors on its
   own notion and misses force-push history).
3. **"Resolve conversation" is a claim, not a fact.** Record it as
   `conditions[].author_resolved`, never as `status: met`. `re-review-delta`
   sets `status` by verifying the diff independently.
4. **Conditions are reconstructed each run** from my posted `issue(blocking)` /
   `issue(non-blocking)` / `suggestion` comments (see
   `config/comment-vocab.md`). Never stored — posted comments ARE the ledger.
5. **Do NOT auto-merge.** Housekeeping surfaces approved PRs with two columns —
   *time since approval* and *did author push since?* Flag
   approved-but-CI-red and approved-needs-2nd-reviewer as `blocked_on=merge`
   (not on me).
   - Note branch-protection **"dismiss stale approvals on new commits"**: if on,
     a late push reopens the gate (re-clear fast); if off, the resident is the
     only thing between a late push and merge under a stale approval — surface
     loudly.
6. **Follow-up capture at merge is an *offer*, never auto-create.** Convert
   still-open **deferred** non-blocking threads (not *declined* ones) into
   tracked issues only on confirmation, carrying the original thread link.

## Acuity vs. effort — two independent axes (§2b)

Never collapse risk into effort. A 1-line auth change is low-effort/high-risk; a
900-line rename is high-effort/low-risk.

**`effort.size_bucket`** is purely mechanical (total changed lines):

| bucket | total lines (additions + deletions) |
|---|---|
| XS | ≤ 10 |
| S | ≤ 50 |
| M | ≤ 200 |
| L | ≤ 600 |
| XL | > 600 |

**`acuity.risk`** — starting heuristics (slice 0; open question §12 to refine
against real PRs). Risk is `high` if ANY of:

- A `config/escalation.yml` rule matches (`escalation.forced` — secrets,
  payments, schema/migrations, public API, breaking-change label, XL size).
- Touches concurrency/unsafe primitives (`unsafe`, lock/mutex, atomic ordering).
- Touches IO correctness paths (file format read/write, serialization,
  checksums).

`risk: med` — core library logic with no high-risk markers. `risk: low` — docs,
comments, tests-only, dependency bumps, config. `urgency` is independent of risk
(a low-risk PR can be release-blocking). `rationale` always states the driver in
one line so an unsupported "high" is conspicuous.

`pr-sync` emits a *deterministic baseline* acuity from paths/size/keywords;
residents (slice 3) may raise it with evidence but the bright-line escalations
are non-negotiable.

## Producer/consumer notes

- `relevance` is populated for triage *candidates* only; `requested: bool`
  (colleague- or self-requested) is known from the timeline before any ML, so it
  is always present even when `score` is absent (pre-slice-4).
- `delta` is `null` outside the `re_review` lane.
- All timestamps are derived into `age_in_state_hrs` by `pr-sync` at emit time;
  consumers never parse raw dates.
