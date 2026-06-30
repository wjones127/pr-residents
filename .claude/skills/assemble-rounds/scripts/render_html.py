"""Render the rounds frame as a self-contained HTML page — the rich,
color-coded sibling of assemble.py's terminal frame.

Same data, same lane ordering (it imports render.py's sort keys so the two
never drift): a reconciliation banner, the self-requested triage panel, and the
three lanes (fresh / re-review / housekeeping). Where a PR has a cached SOAP
workup (Step 4) and/or a drafted disposition (Step 5), they are attached under
the entry in a collapsible <details> block.

No LLM, no GitHub access. Reads only the Store seam. Writes one HTML file.

Usage:
    python3 render_html.py [--state-dir state] [--out state/cache/rounds.html] [--date YYYY-MM-DD]
"""

from __future__ import annotations

import argparse
import datetime as _dt
import html
import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(_HERE, "..", "..", "pr-sync", "scripts"))

import render  # noqa: E402  (shared lane ordering / age formatting)
import store as store_mod  # noqa: E402

import reconcile  # noqa: E402  (sibling, for agreement_rate)


def _esc(s) -> str:
    return html.escape(str(s), quote=True)


def _load_dispositions(store) -> dict:
    """Map (repo, number) -> latest drafted disposition. Dispositions filenames
    are cycle ISO timestamps, so lexical sort puts the newest cycle last."""
    keys = store.list_keys(store_mod.DISPOSITIONS_PREFIX)
    out: dict[tuple[str, int], dict] = {}
    for key in keys:  # oldest-first; later cycles overwrite earlier ones
        doc = store.get_json(key) or {}
        for d in doc.get("dispositions", []):
            out[(d["repo"], int(d["number"]))] = d
    return out


def _load_workup(store, r: dict) -> str | None:
    """The cached SOAP for a record's current head SHA, or None on miss."""
    sha = r.get("head_oid")
    if not sha:
        return None
    doc = store.get_json(store_mod.workup_key(r["repo"], r["number"], sha))
    if not doc:
        return None
    return (doc.get("soap") or "").strip() or None


# ─────────────────────────── HTML fragments ───────────────────────────

_REC_LABEL = {"approve": "APPROVE", "block": "BLOCK", "comment": "COMMENT"}


def _details_block(soap: str | None, disp: dict | None) -> str:
    if not soap and not disp:
        return ""
    rec = (disp or {}).get("recommendation")
    rec_html = ""
    if rec:
        rec_html = f'<span class="rec rec-{_esc(rec)}">{_esc(_REC_LABEL.get(rec, rec.upper()))}</span> '
    blocking = (disp or {}).get("drafted_blocking_count")
    blocking_html = f"{blocking} blocking" if blocking is not None else "workup cached"
    drafted_at = (disp or {}).get("drafted_at")
    when = f" · drafted {_esc(drafted_at[:10])}" if drafted_at else ""

    body_rows = []
    if soap:
        body_rows.append(f'<div class="kv">{_esc(soap)}</div>')
    if disp:
        lane = _esc(disp.get("lane", ""))
        domain = _esc(disp.get("domain", ""))
        bc = disp.get("drafted_blocking_count", "?")
        body_rows.append(
            f'<div class="kv" style="margin-top:8px">Lane: {lane} · '
            f'Domain: <code>{domain}</code> · Drafted blocking conditions: {bc}</div>'
        )
    return (
        '<details class="soap">'
        f'<summary>{rec_html}{_esc(blocking_html)}{when}</summary>'
        f'<div class="soap-body">{"".join(body_rows)}</div>'
        "</details>"
    )


def _ci_class(ci: str) -> str:
    return {"green": "ci-green", "red": "ci-red", "pending": "ci-pending"}.get(ci, "ci-pending")


def _lane_row(r: dict, soap: str | None, disp: dict | None) -> str:
    a = r["acuity"]
    risk = a["risk"].upper()
    size = r["effort"]["size_bucket"]
    ci = r["merge_state"]["ci"]
    escalated = r["escalation"]["forced"]
    esc_badge = '<span class="esc">ESCALATED</span>' if escalated else ""
    return f"""    <div class="row{' escalated' if escalated else ''}">
      <span class="badges"><span class="b acuity-{_esc(risk)}">{_esc(risk)}</span><span class="b size">{_esc(size)}</span></span>
      <span class="title"><a href="{_esc(r['url'])}">{_esc(r['title'])}</a> <span class="num">{_esc(r['repo'])}#{r['number']}</span></span>
      <span class="meta">{esc_badge}<span>{_esc(render._age(r['age_in_state_hrs']))} in state</span><span class="ci {_ci_class(ci)}"><span class="dot"></span>CI {_esc(ci)}</span></span>
      <div class="note">{_esc(a['rationale'])}</div>
      {_details_block(soap, disp)}
    </div>"""


def _house_row(r: dict) -> str:
    blocked = r["blocked_on"]
    if r.get("is_draft"):
        tag = f"draft, waiting on {r['author']}"
    elif blocked == "merge":
        tag = "approved, not merged"
    else:
        tag = f"stale, waiting on {r['author']}"
    ci = r["merge_state"]["ci"]
    return f"""    <div class="row">
      <span class="title"><a href="{_esc(r['url'])}">{_esc(r['title'])}</a> <span class="num">{_esc(r['repo'])}#{r['number']}</span></span>
      <span class="meta"><span>{_esc(tag)} · {_esc(render._age(r['age_in_state_hrs']))}</span><span class="ci {_ci_class(ci)}"><span class="dot"></span>CI {_esc(ci)}</span></span>
    </div>"""


def _triage_row(p: dict) -> str:
    return f"""    <div class="trow">
      <span class="score">{p['score']:.1f}</span>
      <span class="title"><a href="{_esc(p['url'])}">{_esc(p['title'])}</a> <span class="num">{_esc(p['repo'])}#{p['number']}</span> <span class="by">{_esc(p['author'])}</span></span>
      <span class="affinity">{_esc(p['rationale'])}</span>
    </div>"""


def _banner(log: dict | None) -> str:
    if not log or not log.get("domains"):
        return "<b>Reconciliation</b> — no prior cycle yet (learning log starts this run)."
    parts = ["<b>Reconciliation</b> — drafted-vs-posted agreement, per domain (§5; instrument only):"]
    for dom, s in sorted(log["domains"].items(), key=lambda kv: -kv[1].get("samples", 0)):
        rate = reconcile.agreement_rate(s)
        flag = " ⚑anchoring?" if dom in (log.get("anchoring_flags") or []) else ""
        parts.append(
            f"<br><code>{_esc(dom)}</code> agree {_esc(rate)} over {s['samples']} "
            f"(over-call {s['over_call']}, under-call {s['under_call']}){flag}"
        )
    return "".join(parts)


# ──────────────────────────────── page ────────────────────────────────

_CSS = """
:root{--bg:#0f1115;--panel:#171a21;--panel-2:#1d2129;--border:#2a2f3a;--text:#e6e9ef;--muted:#99a1b3;--faint:#6b7280;--link:#6db3f2;--green:#3fb950;--red:#f85149;--amber:#d29922;--fresh:#f78166;--rereview:#6db3f2;--house:#8b949e;--escalate:#db61a2;--approve:#3fb950;--block:#f85149;--comment:#d29922}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;padding:28px 18px 80px}
.wrap{max-width:1040px;margin:0 auto}
h1{font-size:22px;margin:0 0 4px;letter-spacing:-.01em}
.sub{color:var(--muted);margin:0 0 22px;font-size:13px}.sub b{color:var(--text)}
.banner{background:var(--panel);border:1px solid var(--border);border-left:3px solid var(--faint);border-radius:8px;padding:12px 16px;margin-bottom:22px;color:var(--muted);font-size:13px}
.banner code{background:var(--panel-2);padding:1px 5px;border-radius:4px;color:#e6c07b}
section{margin-bottom:30px}
.lane-head{display:flex;align-items:baseline;gap:10px;margin:0 0 12px;padding-bottom:8px;border-bottom:1px solid var(--border)}
.lane-head h2{font-size:16px;margin:0;letter-spacing:.02em}
.lane-head .count{font-size:12px;color:var(--bg);font-weight:700;padding:1px 8px;border-radius:10px}
.lane-head .desc{color:var(--faint);font-size:12px;margin-left:auto}
#triage .count{background:#a371f7}#triage h2{color:#a371f7}
#fresh .count{background:var(--fresh)}#rereview .count{background:var(--rereview)}#house .count{background:var(--house)}
#fresh h2{color:var(--fresh)}#rereview h2{color:var(--rereview)}#house h2{color:var(--house)}
.row{background:var(--panel);border:1px solid var(--border);border-radius:8px;padding:10px 14px;margin-bottom:8px;display:flex;flex-wrap:wrap;align-items:center;gap:10px}
.row:hover{border-color:#3a4150}.row.escalated{border-left:3px solid var(--escalate)}
.badges{display:inline-flex;gap:6px;flex:0 0 auto}
.b{font-size:11px;font-weight:700;letter-spacing:.03em;padding:2px 7px;border-radius:5px;white-space:nowrap}
.acuity-HIGH{background:#3a1518;color:var(--red)}.acuity-MED{background:#3a2f12;color:var(--amber)}.acuity-LOW{background:#22272e;color:var(--muted)}
.size{background:var(--panel-2);color:var(--muted);border:1px solid var(--border)}
.title{flex:1 1 320px;min-width:240px}
.title a{color:var(--link);text-decoration:none;font-weight:600}.title a:hover{text-decoration:underline}
.title .num{color:var(--faint);font-weight:600}
.meta{display:inline-flex;gap:10px;align-items:center;flex:0 0 auto;color:var(--faint);font-size:12px}
.ci{display:inline-flex;align-items:center;gap:5px}
.dot{width:8px;height:8px;border-radius:50%;display:inline-block}
.ci-green .dot{background:var(--green)}.ci-red .dot{background:var(--red)}.ci-pending .dot{background:var(--amber)}
.ci-green{color:var(--green)}.ci-red{color:var(--red)}.ci-pending{color:var(--amber)}
.esc{font-size:10px;font-weight:700;color:var(--escalate);border:1px solid var(--escalate);padding:1px 6px;border-radius:5px}
.note{flex-basis:100%;color:var(--faint);font-size:12px;padding-left:2px}
details{flex-basis:100%;margin-top:6px}
details>summary{cursor:pointer;list-style:none;font-size:12px;font-weight:600;padding:5px 10px;border-radius:6px;background:var(--panel-2);border:1px solid var(--border);display:inline-flex;align-items:center;gap:8px;width:fit-content}
details>summary::-webkit-details-marker{display:none}
details.soap>summary::before{content:"▸ SOAP";color:var(--muted)}
details.soap[open]>summary::before{content:"▾ SOAP"}
details.howto{flex-basis:auto;margin:0 0 22px}
details.howto>summary{background:var(--panel);border-color:var(--border);color:var(--text);width:100%;font-size:13px}
details.howto>summary::before{content:"▸";color:#a371f7;font-weight:700}
details.howto[open]>summary::before{content:"▾";color:#a371f7;font-weight:700}
.howto-body{margin-top:8px;padding:14px 16px;background:var(--panel);border:1px solid var(--border);border-left:3px solid #a371f7;border-radius:8px;font-size:13px;color:var(--muted);line-height:1.6}
.howto-body b{color:var(--text)}
.howto-body code{background:var(--panel-2);padding:1px 6px;border-radius:4px;color:#e6c07b}
.howto-body ol{margin:8px 0 0;padding-left:20px}
.howto-body li{margin-bottom:6px}
.rec{font-weight:700;padding:1px 7px;border-radius:4px;font-size:11px}
.rec-approve{background:#122a18;color:var(--approve)}.rec-block{background:#2d1416;color:var(--block)}.rec-comment{background:#2d2611;color:var(--comment)}
.soap-body{margin-top:8px;padding:12px 14px;background:#0c0e12;border:1px solid var(--border);border-radius:6px;font-size:12.5px;color:var(--text);white-space:pre-wrap}
.soap-body .kv{color:var(--muted)}.soap-body code{background:var(--panel-2);padding:1px 5px;border-radius:4px;color:#e6c07b}
.trow{background:var(--panel);border:1px solid var(--border);border-radius:8px;padding:9px 14px;margin-bottom:7px;display:flex;flex-wrap:wrap;align-items:center;gap:12px}
.trow:hover{border-color:#3a4150}
.score{flex:0 0 auto;font-variant-numeric:tabular-nums;font-weight:700;font-size:13px;color:#c9a3f7;background:#241a33;border:1px solid #3a2a52;padding:2px 9px;border-radius:6px;min-width:52px;text-align:center}
.by{color:var(--faint);font-weight:400;font-size:12px}
.affinity{color:var(--faint);font-size:11.5px;flex:0 0 auto}
footer{color:var(--faint);font-size:12px;margin-top:36px;border-top:1px solid var(--border);padding-top:14px}
"""


_HOWTO = """  <details class="howto">
    <summary>How to draft &amp; co-sign these reviews</summary>
    <div class="howto-body">
      This page is the <b>read-only morning queue</b> — it never writes to GitHub.
      The SOAP reviews (with their <code>issue()</code> / <code>suggestion()</code>
      comments) are drafted and posted in a <b>separate interactive session</b>,
      under your own GitHub identity. The collapsed SOAP blocks below show only a
      cached recommendation + blocking count; the full drafted comments appear in
      that session.
      <ol>
        <li>Open an <b>interactive</b> Claude Code session in the repo
          (<code>~/Documents/pr-residents</code>) — not the scheduled routine,
          which is read-only by design.</li>
        <li>Invoke the <b>assemble-rounds</b> skill. It fans out a per-PR SOAP
          workup for each fresh / re-review PR (cached workups are reused), then
          presents each drafted disposition for your review.</li>
        <li><b>Co-sign each write individually.</b> Nothing posts to GitHub until
          you approve it — that co-sign, under your identity, is the whole point.
          Residents are queryable: push back ("did you check the null case?") and
          the draft is rewritten, not just answered.</li>
      </ol>
    </div>
  </details>"""


def render_html(records, panel, reconcile_log, dispositions, workups, date_label: str) -> str:
    fresh = sorted([r for r in records if r["lane"] == "fresh"], key=render._fresh_key)
    rereview = sorted([r for r in records if r["lane"] == "re_review"], key=render._rereview_key)
    house = [r for r in records if r["lane"] == "housekeeping"]

    def lane_rows(rs):
        rows = []
        for r in rs:
            k = (r["repo"], int(r["number"]))
            rows.append(_lane_row(r, workups.get(k), dispositions.get(k)))
        return "\n".join(rows) or '    <div class="note">(none)</div>'

    triage_html = "\n".join(_triage_row(p) for p in (panel or [])) or '    <div class="note">(none proposed)</div>'

    parts = [
        f"<title>PR Rounds — {_esc(date_label)}</title>",
        f"<style>{_CSS}</style>",
        '<div class="wrap">',
        "  <h1>PR Review Rounds</h1>",
        f'  <p class="sub"><b>{_esc(date_label)}</b> · lance-format/lance + lancedb/lancedb · '
        f'<b>{len(records)} PRs</b> — {len(fresh)} fresh · {len(rereview)} re-review · {len(house)} housekeeping</p>',
        _HOWTO,
        f'  <div class="banner">{_banner(reconcile_log)}</div>',
        # Triage
        '  <section id="triage"><div class="lane-head"><h2>Triage</h2>'
        f'<span class="count">{len(panel or [])}</span>'
        '<span class="desc">self-requested candidates — confirm / strike (pr-relevance)</span></div>',
        triage_html,
        "  </section>",
        # Fresh
        '  <section id="fresh"><div class="lane-head"><h2>Fresh</h2>'
        f'<span class="count">{len(fresh)}</span>'
        '<span class="desc">acuity-ordered · never reviewed, assigned to you</span></div>',
        lane_rows(fresh),
        "  </section>",
        # Re-review
        '  <section id="rereview"><div class="lane-head"><h2>Re-review</h2>'
        f'<span class="count">{len(rereview)}</span>'
        '<span class="desc">ordered by proximity to merge</span></div>',
        lane_rows(rereview),
        "  </section>",
        # Housekeeping
        '  <section id="house"><div class="lane-head"><h2>Housekeeping</h2>'
        f'<span class="count">{len(house)}</span>'
        '<span class="desc">discharge planning, batched</span></div>',
        ("\n".join(_house_row(r) for r in house) or '    <div class="note">(none)</div>'),
        "  </section>",
        '  <footer>Generated by render_html.py from the deterministic pre-assembly '
        "(pr-sync → pr-relevance → reconcile). SOAP blocks shown where a workup is cached for the "
        "current head SHA. Full SOAP reviews are drafted and co-signed in the interactive session.</footer>",
        "</div>",
    ]
    return "\n".join(parts) + "\n"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Render the rounds frame as an HTML page.")
    repo_root = os.path.abspath(os.path.join(_HERE, "..", "..", "..", ".."))
    parser.add_argument("--state-dir", default=os.path.join(repo_root, "state"))
    parser.add_argument("--out", default=None, help="HTML output path (default: <state-dir>/cache/rounds.html)")
    parser.add_argument("--date", default=None, help="Date label for the header (default: today, UTC)")
    args = parser.parse_args(argv)

    store = store_mod.FileStore(args.state_dir)
    records = store.get_json(store_mod.RECORDS) or []
    panel = store.get_json(store_mod.PANEL) or []
    reconcile_log = store.get_json(store_mod.RECONCILE_AGREEMENT)
    dispositions = _load_dispositions(store)
    workups = {(r["repo"], int(r["number"])): _load_workup(store, r) for r in records}
    workups = {k: v for k, v in workups.items() if v}

    date_label = args.date or _dt.datetime.now(_dt.timezone.utc).strftime("%Y-%m-%d")
    page = render_html(records, panel, reconcile_log, dispositions, workups, date_label)

    out = args.out or os.path.join(args.state_dir, "cache", "rounds.html")
    os.makedirs(os.path.dirname(out), exist_ok=True)
    with open(out, "w", encoding="utf-8") as fh:
        fh.write(page)
    print(out)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
