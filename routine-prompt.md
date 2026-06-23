# Routine driver prompt (canonical copy)

This is the **thin** prompt the remote claude.ai routine runs on a schedule. The
real orchestration logic lives in the committed `assemble-rounds` skill, not
here, so it stays versioned and reviewable (§6). Keep this prompt short; paste it
into the routine definition (see `ROUTINE_SETUP.md`).

---

Invoke the `assemble-rounds` skill to assemble today's PR review rounds for the
configured repos.

Hard rules:
- **Read-only. Make zero writes to GitHub** — no reviews, comments, labels,
  assignments, or merges. Everything you produce is a draft for me to co-sign.
- If a repo or PR fails, report it and continue; never abort the whole round.
- When done, persist this cycle's draft dispositions to `state/` and push state
  to the `claude/state` branch so the next run can reconcile against it.

Output the unified rounds view (reconciliation banner, triage panel, three
lanes) with each PR's SOAP workup.
