---
name: fresh-review
description: "Review a fresh-lane PR (assigned to you, never reviewed) from its PRRecord plus the full net diff. Produces a SOAP review: findings tied to ground truth (line / test / call-site), drafted issue()/suggestion() conventional comments, and an approve/block recommendation. Read-only, drafts only. Use for PRs in the fresh lane, or when asked to review a PR for the first time."
---

# fresh-review

The **fresh-lane** resident — the sibling of `re-review-delta` (which owns the
`re_review` lane). For a PR you are reviewing for the FIRST time: there is no
prior conditions ledger, so you read the whole net diff and produce the initial
review.

Read-only. The output is a draft review for the attending to co-sign; never
post.

## Step 1 — build the packet (deterministic)

```sh
set -a; source .env; set +a
python3 .claude/skills/fresh-review/scripts/freshreview.py OWNER/REPO NUMBER --out state/packet.json
```

The packet has `pr` (title / url / head / author / `author_status` / body /
lane), the deterministic baseline (`acuity`, `effort`, `escalation`,
`merge_state` — straight from the same derivations `pr-sync` uses), and `diff`:
the PR's **net** changes vs its merge base (a long-lived branch that merged main
in is NOT swamped by main's churn), each patch truncated at 500 lines.

- `diff.omitted` lists files GitHub gave no patch for (binary / too large). You
  did **not** read those — say so in the assessment.
- If `lane_note` is set, the PR isn't actually fresh (you have a prior review) —
  prefer `re-review-delta`, which verifies your conditions ledger against a
  scoped delta.

## Step 2 — read the diff and find (tied to ground truth)

Produce findings, each anchored to a `file:line` / test / call site in the
packet. This is the open-ended pass — the analog of re-review's fresh-eyes
section, but over the whole PR. Look hardest where the baseline can't see:

- **Refine `acuity.risk` from content** (plan §5). The producer's path-only
  baseline never asserts `high` — that is your job, with evidence: concurrency /
  `unsafe`, IO-correctness (format read/write, serialization, checksums),
  error / panic paths, API or wire-compat breakage. State what you saw and cite
  it. Don't lower the baseline risk without a reason.
- Respect `escalation`: if `forced`, this PR gets full rounds regardless of how
  clean it looks — name the rule. Escalation is routing, not a risk score; keep
  the two axes separate.
- An unsupported assertion must be conspicuous. If you claim "covered by tests,"
  point at the test; if you didn't verify something, say you didn't.

## Step 3 — draft comments in the conditions vocabulary

Phrase each finding as a conventional comment (`config/comment-vocab.md` and the
`conventional-comments` skill) — those labels ARE the conditions ledger a later
re-review reconstructs:

- `issue(blocking)` — a merge condition. State the **acceptance criterion** (how
  the author knows it's cleared) so re-review can verify it against the diff.
- `issue(non-blocking)` / `suggestion` — author's call, not gating.
- `question` — gates your assessment until answered.

Use the blocking token deliberately (over-calling reads as timid, under-calling
as reckless). **Self-conditions dissolve**: a thing *you* need convincing of —
satisfy it this sitting (trace the call graph / covering test) or convert it
into a concrete `issue(...)` ask. Never post a vague worry.

## Step 4 — present (draft, never post)

```
REVIEW · {repo}#{number} — {title}
{author} ({author_status}) · {size_bucket} (+{add}/-{del}, {files} files){  ⚠ escalation}

ONE-LINER
  {what changes and why}

FINDINGS  (each tied to ground truth)
  • {file:line} — {what you checked / what you found}
  • {covered by <test> | NOT covered} ...

ASSESSMENT
  risk: {low|med|high} · urgency: {…}  — {rationale, refined from the diff}
  escalation: {forced on <rules> → full rounds | none}
  could not read: {omitted files, or "none"}

DRAFT COMMENTS
  issue(blocking): {subject} — acceptance: {how the author clears it}
  suggestion: {…}

RECOMMENDATION: {approve / block on N conditions / needs a sitting}
  confidence: {…} · what would make this wrong: {…}
```

Verify against the patch, not your own prose. Keep the two halves — what you
checked (Findings) and what you're asking for (Draft comments) — visually
distinct. Attention is the scarce resource: surface what changes the decision.
