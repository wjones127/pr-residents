"""The deterministic rounds frame: a reconciliation banner + the self-requested
triage panel + the three lanes. This is the navigational skeleton the
assemble-rounds orchestrator fills with per-PR SOAP workups (from the
fresh-review / re-review-delta residents). No LLM, no GitHub access.

Usage:
    python3 assemble.py [--records state/records.json] [--panel state/panel.json]
                        [--state-dir state]
"""

from __future__ import annotations

import argparse
import json
import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(_HERE, "..", "..", "pr-sync", "scripts"))

import render  # noqa: E402  (the three-lane renderer)

import reconcile  # noqa: E402  (sibling, for agreement_rate)


def _load(path: str | None):
    if path and os.path.exists(path):
        try:
            return json.load(open(path, encoding="utf-8"))
        except (OSError, ValueError):
            return None
    return None


def reconciliation_banner(log: dict | None) -> str:
    if not log or not log.get("domains"):
        return "RECONCILIATION — no prior cycle yet (logging starts this run)"
    out = ["RECONCILIATION — drafted-vs-posted agreement, per domain (§5; instrument only)"]
    for dom, s in sorted(log["domains"].items(), key=lambda kv: -kv[1].get("samples", 0)):
        rate = reconcile.agreement_rate(s)
        flag = " ⚑anchoring?" if dom in (log.get("anchoring_flags") or []) else ""
        out.append(f"  {dom}: agree {rate} over {s['samples']} "
                   f"(over-call {s['over_call']}, under-call {s['under_call']}){flag}")
    return "\n".join(out)


def triage_panel(panel: list[dict] | None) -> str:
    if not panel:
        return "TRIAGE — self-requested candidates: none proposed"
    out = [f"TRIAGE — {len(panel)} self-requested candidates (confirm/strike; pr-relevance)"]
    for p in panel:
        out.append(f"  [{p['score']:>5}] {p['repo']}#{p['number']}  {p['title'][:60]}")
        out.append(f"          by {p['author']} · {p['rationale']}")
    return "\n".join(out)


def assemble(records: list[dict] | None, panel: list[dict] | None,
             reconcile_log: dict | None) -> str:
    blocks = [
        reconciliation_banner(reconcile_log),
        "",
        triage_panel(panel),
        "",
        render.render(records or []),
    ]
    return "\n".join(blocks)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Render the deterministic rounds frame.")
    repo_root = os.path.abspath(os.path.join(_HERE, "..", "..", "..", ".."))
    state = os.path.join(repo_root, "state")
    parser.add_argument("--records", default=os.path.join(state, "records.json"))
    parser.add_argument("--panel", default=os.path.join(state, "panel.json"))
    parser.add_argument("--state-dir", default=state)
    args = parser.parse_args(argv)

    records = _load(args.records) or []
    panel = _load(args.panel) or []
    reconcile_log = _load(os.path.join(args.state_dir, "reconcile", "agreement.json"))
    print(assemble(records, panel, reconcile_log))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
