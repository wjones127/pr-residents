"""The deterministic rounds frame: a reconciliation banner + the self-requested
triage panel + the three lanes. This is the navigational skeleton the
assemble-rounds orchestrator fills with per-PR SOAP workups (from the
fresh-review / re-review-delta residents). No LLM, no GitHub access.

Usage:
    python3 assemble.py [--state-dir state]
"""

from __future__ import annotations

import argparse
import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(_HERE, "..", "..", "pr-sync", "scripts"))

import render  # noqa: E402  (the three-lane renderer)
import store as store_mod  # noqa: E402

import reconcile  # noqa: E402  (sibling, for agreement_rate)


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
    parser.add_argument("--state-dir", default=os.path.join(repo_root, "state"))
    args = parser.parse_args(argv)

    store = store_mod.FileStore(args.state_dir)
    records = store.get_json(store_mod.RECORDS) or []
    panel = store.get_json(store_mod.PANEL) or []
    reconcile_log = store.get_json(store_mod.RECONCILE_AGREEMENT)
    print(assemble(records, panel, reconcile_log))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
