"""Render PRRecord JSON as the three rounds lanes (§3). Thin; no LLM.

Fresh: acuity-ordered, individual. Re-review: proximity-to-merge ordered.
Housekeeping: batched. Reads sync.py JSON from a file or stdin.

Usage:
    python3 sync.py | python3 render.py
    python3 render.py records.json
"""

from __future__ import annotations

import json
import sys

import derive

_RISK_RANK = {"high": 0, "med": 1, "low": 2}
_URGENCY_RANK = {"high": 0, "med": 1, "low": 2}
_CI_RANK = {"green": 0, "pending": 1, "red": 2}


def _fresh_key(r: dict):
    a = r["acuity"]
    return (_RISK_RANK.get(a["risk"], 3), _URGENCY_RANK.get(a["urgency"], 3),
            -r["age_in_state_hrs"])


def _rereview_key(r: dict):
    # Proximity to merge: CI green first, then fewer open changes, then older.
    ci = r["merge_state"]["ci"]
    return (_CI_RANK.get(ci, 3), -r["age_in_state_hrs"])


def _age(hrs: float) -> str:
    if hrs >= 48:
        return f"{hrs / 24:.0f}d"
    return f"{hrs:.0f}h"


def _needs_author_reason(r: dict) -> str:
    """Why an approved PR isn't ready — ordered by which blocker to name first.
    Conflict wins over CI (a rebase re-runs CI anyway)."""
    ms = r["merge_state"]
    if ms.get("mergeable") is False:
        return "needs rebase (conflict)"
    if ms.get("ci") == "red":
        return "CI failing"
    if ms.get("ci") == "pending":
        return "CI running"
    if r.get("review_decision") in ("REVIEW_REQUIRED", "CHANGES_REQUESTED"):
        return "awaiting other approvals"
    if ms.get("mergeable") is None:
        return "checking mergeability"
    return "not mergeable yet"


def _line(r: dict) -> str:
    a = r["acuity"]
    flag = " ⚠ESCALATED" if r["escalation"]["forced"] else ""
    return (f"  [{a['risk'].upper():<4} {r['effort']['size_bucket']:<2}] "
            f"{r['repo']}#{r['number']}  {r['title'][:70]}{flag}\n"
            f"        {r['url']}  · {_age(r['age_in_state_hrs'])} in state · "
            f"CI {r['merge_state']['ci']} · {a['rationale']}")


def render(records: list[dict]) -> str:
    fresh = sorted([r for r in records if r["lane"] == "fresh"], key=_fresh_key)
    rereview = sorted([r for r in records if r["lane"] == "re_review"], key=_rereview_key)
    house = [r for r in records if r["lane"] == "housekeeping"]

    out: list[str] = []
    out.append(f"━━ ROUNDS ━━  {len(records)} PRs  "
               f"({len(fresh)} fresh · {len(rereview)} re-review · {len(house)} housekeeping)\n")

    out.append(f"\n▌FRESH ({len(fresh)}) — acuity-ordered")
    for r in fresh:
        out.append(_line(r))
    if not fresh:
        out.append("  (none)")

    out.append(f"\n▌RE-REVIEW ({len(rereview)}) — ordered by proximity to merge")
    for r in rereview:
        out.append(_line(r))
    if not rereview:
        out.append("  (none)")

    out.append(f"\n▌HOUSEKEEPING ({len(house)}) — discharge planning, batched")
    if not house:
        out.append("  (none)")
    else:
        buckets = {"ready": [], "needs_author": [], "stale_author": []}
        for r in house:
            buckets[derive.housekeeping_bucket(r)].append(r)

        def _house_line(r: dict, tag: str) -> str:
            return (f"    {r['repo']}#{r['number']}  {r['title'][:60]}  "
                    f"[{tag} · {_age(r['age_in_state_hrs'])} · CI {r['merge_state']['ci']}]")

        def _subsection(label: str, rows: list, tag_of) -> None:
            out.append(f"\n  {label} ({len(rows)})")
            for r in rows:
                out.append(_house_line(r, tag_of(r)))
            if not rows:
                out.append("    (none)")

        _subsection("① Ready to merge", buckets["ready"], lambda r: "ready")
        _subsection("② Approved — needs author", buckets["needs_author"],
                    _needs_author_reason)
        _subsection(
            "③ Waiting on author", buckets["stale_author"],
            lambda r: (f"draft, waiting on {r['author']}" if r.get("is_draft")
                       else f"stale, waiting on {r['author']}"),
        )

    return "\n".join(out)


def main(argv: list[str] | None = None) -> int:
    argv = argv if argv is not None else sys.argv[1:]
    if argv and argv[0] != "-":
        with open(argv[0], "r", encoding="utf-8") as fh:
            records = json.load(fh)
    else:
        records = json.load(sys.stdin)
    print(render(records))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
