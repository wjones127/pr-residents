# Implementation plan

Port the Python skills repo into a single pure-Go binary (`residents`) that serves a
localhost web UI, per the [README](../README.md). README-driven: this plan implements
what the README already describes.

## Package layout

```
cmd/residents/            main; CLI verbs: init, serve, refresh, dispatch, gc
internal/config/          load + validate config.yml
internal/secrets/         token resolution: env → keychain → 0600 file
internal/github/          GraphQL client            (port github.py)
internal/pipeline/        record, derive, relevance, render  (port sync/derive/relevance/render)
internal/store/           cache/ + ledger/ seam; SQLite via modernc.org/sqlite
internal/jobs/            async supervisor; progress events; cancel
internal/agent/           WorkupAgent seam + claude impl; packet builders (port freshreview/rereview/conditions)
internal/session/         SessionLauncher: worktree + primer
internal/web/             HTTP handlers, SSE, html/template, embedded htmx
  templates/              ported from render_html.py
review/                   go:embed'd review prompts (fresh, re-review, conventional-comments)
testdata/golden/          Python-captured fixtures for parity
```

Module: `github.com/lancedb/pr-residents`. Deps kept minimal: `modernc.org/sqlite`
(pure-Go), `github.com/zalando/go-keyring`, `gopkg.in/yaml.v3`. htmx vendored/embedded
(CSP-safe, no CDN).

## What ports vs. what stays a prompt

- **Ports to Go (runs in the binary):** the whole deterministic pipeline — fetch,
  derive lanes/acuity/blocked-on, relevance (affinity + optional Ollama embed), render,
  store, workup-cache keying, **and the packet builders** (`freshreview.py`,
  `rereview.py`, `conditions.py`). All GitHub I/O + determinism lives in one place.
- **Stays LLM-facing (embedded prompt, fed to the agent):** the review *judgment*
  frameworks (`fresh-review`, `re-review-delta`, `conventional-comments` SKILL bodies).
  The agent receives a Go-built packet + these instructions and returns a SOAP. It does
  no GitHub fetching and needs no Python at runtime.

Reconciliation (`reconcile.py`) and `state_sync.py` are **not** ported — reconciliation
is deferred; the git round-trip is obsolete for a local persistent app.

## Agent hook contract

```go
type WorkupAgent interface {
    Workup(ctx context.Context, p Packet, model string) (SOAP, error)
}
```

Claude impl: spawn `claude -p --output-format json --model <model>` in a scratch dir;
prompt = embedded review framework + the packet; parse SOAP from the result; validate
schema, one retry on malformed. `model` chosen per PR by the config `model_routing`
table (lane/size → model). Codex/Copilot = later impls of the same interface.

```go
type SessionLauncher interface {
    Prepare(ctx, pr PRRef, notes SOAP) (LaunchSpec, error)  // worktree + primer + command
}
```

## Jobs, progress, cancel

- One `jobs.Manager`; `Refresh` and `Dispatch` are async jobs, each with a
  `context.CancelFunc`. Progress + token counts published on a channel → `GET /events`
  (SSE) → htmx.
- **Dispatch** runs `concurrency` (default 6) workers over cache-missing PRs. Each
  completed SOAP is **persisted immediately** to the store.
- **Cancel** = cancel the job context: stop dequeuing, kill in-flight `claude` procs,
  discard their partials. Already-completed SOAPs stay. Token meter = running sum of
  per-run `usage` from each completed/in-flight run's JSON.

## Phases

**Phase 0 — pipeline port + parity.** Module skeleton, `internal/{config,github,
pipeline,store}`, `residents refresh` writing `records.json`/`panel.json`. Secrets via
env only (keychain deferred to P4). Capture current Python outputs on fixtures →
`testdata/golden/`; Go must match. Translate the correctness-trap unit tests
(`headRefOid` identity, re-review delta anchor, stale-approval re-surface).
*Done when:* Go output matches Python golden on all fixtures; ported unit tests pass.
Delete the ported Python modules.

**Phase 1 — web + Refresh (first dogfood).** Store seam on disk; port `render_html.py`
→ Go templates; serve localhost; **Refresh** button → job → SSE progress → rendered
lanes. Config hand-edited; token from env/keychain read.
*Done when:* refresh from the browser reproduces today's rounds view. Delete
`render_html.py`.

**Phase 2 — agent hook + Dispatch.** Port packet builders; embed review prompts;
`WorkupAgent` claude impl; workup cache (head-SHA key); `Dispatch` button with parallel
workers, token meter, cancel. `model_routing` wired.
*Done when:* Dispatch produces SOAPs matching a hand-run resident on 2–3 sample PRs;
cancel keeps completed, drops partials. Delete `freshreview.py`/`rereview.py`/
`conditions.py`; the SKILL bodies live on as embedded prompts.

**Phase 3 — Talk.** `SessionLauncher`: `git worktree add` PR head, write `.pr-primer.md`
(SOAP + diff summary + metadata), return copyable command; per-PR button + modal.
`residents gc` prunes stale worktrees.
*Done when:* button yields a working command that drops into a primed session.

**Phase 4 — onboarding + secrets + polish.** First-run wizard (repos → username →
live-validated tokens → engine detection → optional interests/Ollama); keychain write
via go-keyring + `0600` fallback; `residents init`. Cross-platform check.
*Done when:* a colleague goes install → wizard → first round with no hand-editing.

## Testing / parity strategy

- **Golden parity** is the port's safety net: freeze Python outputs on recorded GitHub
  fixtures (no live API in tests), diff Go against them until equal, then delete the
  Python. Never delete a module before its Go port is green.
- Correctness-trap tests ported first and kept as the regression floor.
- Agent-dependent stages (Dispatch/Talk) tested against recorded packets + a fake
  `WorkupAgent`; the real claude path smoke-tested manually on sample PRs.

## Open questions

1. Repo strategy: evolve this repo in place (Python deleted phase-by-phase) vs. fresh
   repo importing fixtures? Leaning in-place.
2. Packet builders → Go (this plan) genuinely preferred over letting the skill build its
   own packet inside the agent session? The former is more port work now but keeps the
   agent Python-free and all GitHub I/O in the binary.
3. SOAP transport out of headless claude: parse from `--output-format json` result, or
   have the agent write SOAP to a known file? (Leaning: parse result + schema-validate.)
4. `config.yml` — keep team-shared `escalation.yml` / `comment-vocab.md` as separate
   committed policy files, or fold into per-user config? (Deferred from README review.)
5. Worktree location + cleanup policy for Talk — auto-gc after N days, or manual
   `residents gc` only?
