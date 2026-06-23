---
name: conventional-comments
description: "The drafting contract for review comments — how a resident writes review feedback in the attending's voice using conventional-comment labels and blocking-token discipline. Use when drafting PR review comments, deciding blocking vs non-blocking, or composing a review summary. All output is a DRAFT for the attending to co-sign; never post."
---

# conventional-comments

The **producing** half of the conditions model (`re-review-delta` is the
consuming half). Every drafted review comment uses the labels in
`config/comment-vocab.md`, because those posted labels ARE the conditions ledger
that re-review later reconstructs — no separate store.

**Drafts only.** This skill never posts to GitHub. It produces comment text the
attending edits and co-signs. The draft-vs-posted diff is the signal
graduated-autonomy measures later (slice 5), so drafts must be in the
attending's voice and use labels deliberately.

## The contract (see config/comment-vocab.md for the table)

`<label>(<decoration>): <subject>`

- `issue(blocking)` — **a merge condition.** Must be resolved before merge.
- `issue(non-blocking)` — should fix, author's call. Not gating.
- `suggestion` — optional improvement. Not gating.
- `question` — needs an answer to assess; may become blocking once answered.
- `nitpick` / `praise` — trivial / positive. Not conditions.

## Blocking-token discipline

The one high-judgment thing the attending co-signs shrinks to `(blocking)` vs
`(non-blocking)`. Get it right:

- **Blocking** = correctness, safety, data loss, API/compat breakage, security,
  a test that no longer tests what it claims. Things that would be a mistake to
  merge.
- **Non-blocking / suggestion** = style, naming, refactors, "nice to have,"
  preferences. The author may decline.
- **Over-calling** (marking blocking when it's taste) reads as timid and erodes
  the author's trust in the label. **Under-calling** (soft-pedalling a real
  defect) is reckless. When unsure, say what would make it blocking.

## Self-conditions dissolve

A thing *you* need to be convinced of ("I want to be sure this lock ordering
can't deadlock") is NOT posted as-is. Either:
- satisfy it this sitting (the resident pre-stages the call graph / covering
  tests / concurrent paths so you can), or
- convert it into a concrete `issue(...)` ask to the author.

Never post a vague worry; post a verifiable condition or resolve it yourself.

## Drafting shape

1. Anchor each comment to ground truth — a line, a test, a call site.
2. Lead with the label; keep the subject one crisp sentence.
3. For blocking issues, state the acceptance criterion (how the author knows
   it's resolved), so re-review can verify it against the diff.
