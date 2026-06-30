# Routine driver prompt (canonical copy)

This is the **thin** prompt the remote claude.ai routine runs on a schedule. The
real orchestration logic lives in the committed `assemble-rounds` skill, not
here, so it stays versioned and reviewable (§6). Keep this prompt short; paste it
into the routine definition (see `ROUTINE_SETUP.md`).

---

Invoke the `assemble-rounds` skill to assemble today's PR review rounds for the
configured repos.

Hard rules:
- **Make zero writes to GitHub's review surface** — no reviews, comments, labels,
  assignments, or merges. Everything you produce is a draft for me to co-sign.
  (The skill *does* push per-user state to the `claude/state` branch via the
  clone's git credentials — that is state persistence, not a review write.)
- If a repo or PR fails, report it and continue; never abort the whole round.
- The skill hydrates `state/` from `claude/state` at the start and persists it
  back at the end (Steps 0 and 7); don't skip them, or reconciliation and the
  caches reset every run.

Output the unified rounds view (reconciliation banner, triage panel, three
lanes) with each PR's SOAP workup. Also publish the skill's HTML render
(`state/cache/rounds.html`, Step 6) as an Artifact and share the link — the
color-coded, browsable copy of the same rounds.
