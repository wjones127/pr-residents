# `PRRecord` — the producer/consumer contract

`pr-sync` (slice 1, deterministic, no LLM) is the **sole producer**. Everything
downstream — renderer, residents (`fresh-review`, `re-review-delta`),
`pr-relevance`, `assemble-rounds` — **consumes** this and does not re-fetch PR
*metadata* from GitHub. (Diff *content* — patches — is intentionally not on the
record; the residents' packet builders fetch it.)

Pinning this now (slice 0) is the interface lock: consumers code against these
field semantics, not against GraphQL.

## Shape

```
PRRecord {
  repo              : "owner/name"
  number            : int
  url               : string
  title             : string
  body              : string                              # PR description (triage input)
  author            : string                              # GitHub login
  author_status     : first_time | infrequent | regular | core | unknown
  files_changed     : [path...]                           # net changed paths (≤100)

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
> ride on the record so consumers can't accidentally fast-track a forced PR.
> Escalation is a **routing** signal, NOT a risk score — the two axes stay
> separate (§5). `escalation.forced` means the PR can never be fast-tracked
> (the skim lane, slice 5) and always appears in rounds with the ⚠ flag — but
> it does **not** change which rounds lane the PR lands in. Lane is purely
> `blocked_on`-driven: an escalated PR with new commits since my review stays
> in `re_review` (collapsing it to `fresh` would drop its conditions ledger,
> which high-stakes PRs need most). `acuity.risk` is likewise scored
> independently: a forced escalation on a docs-only PR is still low risk.

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

Escalation does not change lane (see the `escalation` note above): it only
blocks fast-tracking and sets the ⚠ flag.

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

**`acuity.risk`** — scored on the change itself, **independent of escalation**
(escalation is routing, not risk). Starting heuristics (open question §12 to
refine against real PRs):

- `risk: low` — docs, comments, tests-only, dependency bumps, config.
- `risk: med` — core library logic (the baseline `pr-sync` default).
- `risk: high` — reserved for resident refinement (slice 3) reading diff
  content: concurrency/`unsafe` primitives, IO-correctness paths (format
  read/write, serialization, checksums). `pr-sync`'s path-only baseline does not
  assert `high` on its own — it cannot see content, and over-asserting `high`
  flattens the acuity ordering (lance is `.proto`/`.pyi`-heavy, so escalation
  fires often; that must not masquerade as risk).

`urgency` is independent of risk (a low-risk PR can be release-blocking).
`rationale` always states the driver in one line so an unsupported score is
conspicuous.

`pr-sync` emits a *deterministic baseline* acuity from paths/size/keywords;
residents (slice 3) may raise it with evidence but the bright-line escalations
are non-negotiable.

## Triage inputs (slice 3)

`pr-triage` (a resident) consumes the record and re-fetches **nothing**, so the
producer carries the few fields triage needs beyond the universal ones:

- **`body`** — the PR description, for the one-liner and to spot referenced
  issues / intent.
- **`author_status`** — derived from the author's prior *merged* PRs in this
  repo (`first_time` 0, `infrequent` 1–3, `regular` 4–10, `core` >10). Counted
  once per author per run via search. `unknown` when the count couldn't be
  fetched — never guessed. Drives the `needs_ci_approval` recommendation and the
  first-time welcome draft. Cached with the record, so it can lag a slow-moving
  contributor between runs; acceptable for a triage hint.
- **`files_changed`** — the net changed paths, so triage maps components and
  scope without a diff fetch. Same list `escalation` and `acuity` are computed
  from, exposed for the resident to read.

## Producer/consumer notes

- `relevance` is populated for triage *candidates* only; `requested: bool`
  (colleague- or self-requested) is known from the timeline before any ML, so it
  is always present even when `score` is absent (pre-slice-4).
- **My own PRs are never surfaced** (`author == viewer` ⇒ dropped). This is a
  review tool; tracking my own authored work is a separate concern with an
  inverted framing (I am the author waiting on reviewers/CI), out of scope here.
- `delta` is `null` outside the `re_review` lane.
- All timestamps are derived into `age_in_state_hrs` by `pr-sync` at emit time;
  consumers never parse raw dates.
