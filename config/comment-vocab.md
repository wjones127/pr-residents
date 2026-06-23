# Conventional comments contract (SHARED)

The vocabulary every resident drafts in and `re-review-delta` parses. Your
posted review comments already follow this format, so re-review reconstructs the
conditions ledger from real history with no separate store (§2c).

## Labels

Each top-level review comment starts with a label and an optional decoration:

`<label>(<decoration>): <subject>`

| Label | Meaning | Is a merge condition? |
|---|---|---|
| `issue(blocking)` | Must be resolved before merge. | **Yes** — the conditions ledger. |
| `issue(non-blocking)` | Should fix, but author's call. | No — author's choice. |
| `suggestion` | Optional improvement. | No — author's choice. |
| `question` | Needs an answer to assess. | Soft — may become blocking once answered. |
| `nitpick` | Trivial / style. | No. |
| `praise` | Positive callout. | No. |

## Rules

- **The blocking token is the one high-judgment thing you co-sign** (§5). It is
  the signal graduated-autonomy measures (over-calling = timid, under-calling =
  reckless), so resident drafts must use `(blocking)` vs `(non-blocking)`
  deliberately, never as filler.
- **Self-conditions dissolve** (§2c): a thing *you* want to be convinced of
  (e.g. "lock ordering can't deadlock") is not posted as-is. Either satisfy it
  this sitting, or convert it to a concrete `issue(...)` ask to the author.
- **`resolve conversation` is the author's claim, not a fact** (§8). A resolved
  thread does not mark a condition met; `re-review-delta` verifies against the
  diff independently.
- Condition status vocabulary used downstream: `met` / `not_met` / `moot`
  (author took a different approach) / `open`.
