# Demo walkthrough — Slices 1 & 2 (the engine)

This milestone proves the **deterministic engine** is sound before we layer
residents, drafting, and the nightly routine on top. Two capabilities:

1. **Daily triage** (slice 1) — a correct, cross-repo, lane-split review queue
   derived from the GraphQL timeline. Zero ML, zero GitHub writes.
2. **Delta re-review** (slice 2) — for a PR you've already reviewed, reconstruct
   your conditions ledger from your own posted comments and verify each one
   against the *actual diff since you last looked* — treating GitHub's "resolved"
   as a claim, not a fact.

Everything here is read-only. No comment is posted; nothing is merged.

---

## Setup (one-time, ~10s)

```sh
cd pr-residents
set -a; source .env; set +a      # loads GITHUB_TOKEN_LANCE_FORMAT (read-only PAT)
```

That's the whole dependency surface — stdlib Python, one read-only token. No
pip, no venv (so it runs unchanged in the remote sandbox later).

---

## Part 1 — Daily triage (slice 1)

```sh
python3 .claude/skills/pr-sync/scripts/sync.py --out state/records.json
python3 .claude/skills/pr-sync/scripts/render.py state/records.json
```

You get three lanes:

```
━━ ROUNDS ━━  22 PRs  (0 fresh · 9 re-review · 13 housekeeping)

▌FRESH (0) — acuity-ordered
▌RE-REVIEW (9) — ordered by proximity to merge
  [MED  XL] lance-format/lance#6255  feat: add transaction-level table_metadata… ⚠ESCALATED
        … · 61d in state · CI green · core library change; resident to refine…
▌HOUSEKEEPING (13) — discharge planning, batched
  lance-format/lance#7289  feat(schema_evolution)…  [approved, not merged · 6d · CI green]
  lance-format/lance#5368  feat(io): support HDFS…  [stale, waiting on fMeow · 188d · CI green]
```

**Talking points — what to point at:**

- **`blocked_on` is the spine.** Every open PR is reduced to *who it waits on*:
  `me` (→ fresh / re-review), `merge` (approved, not merged → housekeeping),
  `author` (you're waiting on them; only surfaced once stale). This is a pure
  timeline derivation, the reliable high-leverage win.
- **Two independent axes per PR.** `[MED  XL]` = risk `med`, effort `XL`. A
  1-line auth change is low-effort/high-risk; a 900-line rename is the reverse.
  Never collapsed.
- **`⚠ESCALATED` is routing, not risk.** Bright-line rules (secrets, schema,
  public API, XL blast radius) flag a PR as "never fast-track / always full
  rounds." It rides *alongside* the risk score, doesn't overwrite it — so the
  acuity ordering still means something. (We caught and fixed a bug here: the
  flag must not collapse a re-review into a fresh look.)
- **Correctness traps it owns silently:** it anchors "did the author push since
  I looked?" on commit-SHA identity, not GitHub's `pushedDate` (which is dead —
  returns null for everyone) and not `committedDate` (which rebases scramble).
  Approved-but-CI-red is `blocked_on=merge`, never auto-merged.
- **It's incremental:** re-run it — second run is ~10× faster (SQLite cache,
  keyed on `updatedAt`, auto-invalidated when policy changes).

---

## Part 2 — Delta re-review (slice 2)

Pick any PR from the **re-review** lane and build its packet:

```sh
python3 .claude/skills/re-review-delta/scripts/rereview.py lance-format/lance 7367 --out state/packet.json
```

This deterministically (a) reconstructs your conditions ledger from your posted
`issue(blocking)` comments and (b) fetches the diff `last_reviewed_sha…head` —
the commits the author pushed *since your review*.

Then the resident reads the packet and produces the two-section re-review. Here
is the real output for **lance #7367** (a snapshot — your live numbers will
differ):

```
RE-REVIEW · lance-format/lance#7367 — perf(merge_insert): defer reading non-source columns
reviewed c2d76d0b → c3055c8f   (3 commits, 1 file)

CONDITIONS LEDGER (1/2 met)

  ✓ met      [blocking] Comments carry too much use-case context; make them generic.
             → late_take.rs: the rewritten try_new / PartialOrd doc comments now
               describe mechanics generically, no issue-specific context.

  ✗ not_met  [blocking] Worried about matching on string column names and ignoring
             the TableReference.            (author marked RESOLVED — not confirmed)
             → The refactor still derives `deferred_columns` by matching dataset
               field NAMES (HashSet<&str>; the qualifier is dropped at `.map(|(_, f)|`).
               It mitigates with a name-collision bail, but does not honor the
               TableReference as asked. Resolution is a workaround, not a fix.

FRESH-EYES DELTA
  The new collision-bail path ("if two referenced columns share a name, bail") is
  the load-bearing safety net for the above. New concern: confirm it is covered by
  a test — that's now the thing standing between a name collision and wrong data.

RECOMMENDATION: block on 1 condition (#2). confidence: high on the ledger,
medium on the fresh-eyes pass pending the bail-path test.
  what would make this wrong: if a higher layer guarantees unique referenced
  names, condition #2 is moot rather than not-met.
```

**Talking points — the "aha":**

- **GitHub said both conditions were resolved. They are not equal.** Condition 1
  is genuinely met; condition 2 was *mitigated, not honored*. The "resolved"
  checkbox flattened that distinction — the diff verification recovered it. This
  is the single most important thing in the demo.
- **The ledger is reconstructed from your real comments**, in your vocabulary —
  no separate tracker, no manual bookkeeping. Your `issue(blocking):` posts *are*
  the ledger.
- **Two sections never collapse.** The closed-ended ledger tells you whether the
  things you already flagged got handled. The open-ended fresh-eyes pass looks
  for *new* problems in code that changed since — here, it flagged that the
  collision-bail is now untested and load-bearing, which was never on the
  original list.
- **It's a draft.** The recommendation, the block call, "what would make this
  wrong" — all staged for you to edit and co-sign. Nothing is posted.

---

## What this milestone is NOT (yet)

- **No drafting of the actual comment text / SOAP work-ups** — that's slice 3
  (residents) and the `conventional-comments` producer side in anger.
- **No relevance ranking** for self-requested PRs — slice 4 (`pr-relevance`).
- **No nightly routine, no co-sign loop, no fast-track lane** — slice 5.
- `pr-sync` emits `acuity.risk` from paths only (everything code-ish is `med`);
  residents reading diff *content* refine it later. That's why the fresh/re-review
  lanes look uniformly `MED` today.

The point of this checkpoint: if `blocked_on`, the conditions ledger, or the
delta verification were wrong, you'd want to catch it *now* — before residents
and drafting are built on top.

---

## Reproduce the validation

```sh
python3 .claude/skills/pr-sync/tests/test_derive.py          # 21 tests
python3 .claude/skills/re-review-delta/tests/test_conditions.py  # 10 tests
```
