# Routine setup (~2 minutes, per user)

The PR-residents routine is **strictly per-user and not shareable**: it runs
under *your* GitHub identity, so every draft you later co-sign is your own merge.
There is no shared bot account — that is the accountability model (§6, §9). Each
teammate recreates the routine from this doc.

## 1. Make a read-only, fine-grained PAT (per org)

Fine-grained PATs are single-owner, so make one **per organization** you review
in. Scope it to the target repos with **read-only** permissions:

- Repository → **Contents: Read**, **Pull requests: Read**, **Metadata: Read**.
- Nothing writable. The routine never needs write; a read-only token means a
  leak can't post or merge as you (§6: env vars aren't a secrets store).

## 2. Local config

- Copy `config/user.example.yml` → `config/user.yml` (gitignored) and set
  `github_login`, `subscribed_repos`, and optional `interests` (cold-start
  relevance).
- Put tokens in env, named per org: `GITHUB_TOKEN_<ORG>` (org upper-snake-cased,
  e.g. `lance-format` → `GITHUB_TOKEN_LANCE_FORMAT`). Locally that's `.env`
  (gitignored); in the routine it's an environment variable on the routine.

Verify locally before scheduling anything:

```sh
set -a; source .env; set +a
python3 .claude/skills/pr-sync/scripts/sync.py --out state/cache/records.json  # should write N records
python3 .claude/skills/assemble-rounds/scripts/assemble.py                     # should render the frame
```

## 3. Create the remote routine on claude.ai

- New scheduled routine, pointed at this repo (remote clones it; it cannot see
  `~/.claude/`, which is why every skill is repo-committed).
- **Driver prompt:** paste the contents of `routine-prompt.md`. Keep it thin —
  the logic is the `assemble-rounds` skill, versioned here.
- **Environment:** add `GITHUB_TOKEN_<ORG>` for each org. (Optional: semantic
  relevance (`--semantic`) needs a local Ollama embedder, which the cloud routine
  won't have — it degrades to path-affinity there. Run `--semantic` locally so
  vectors land in `claude/state`; set `OLLAMA_HOST` only if not the default
  `localhost:11434`. No embedding API key, anywhere — Anthropic has no embeddings
  endpoint; the model is local.)
- **Schedule:** nightly, before your morning (e.g. 06:00 local). Remote runs with
  the laptop closed; local scheduled tasks don't (they need the app open and the
  machine awake), which is why this is a cloud routine.

## 4. State write-back

Per-user state lives under `state/` and must persist between runs — a remote run
is a fresh clone, so without this every night starts cold. The `assemble-rounds`
skill handles it via `state_sync.py`: **hydrate** at the start (fetch
`claude/state`, unpack it) and **persist** at the end (commit + push).

Two namespaces, two policies:
- `state/cache/` — GitHub-derived (PRRecords, the relevance profile, the
  PR-detail SQLite, the workup SOAPs). Best-effort; stale workups are pruned.
- `state/ledger/` — your drafts and the accumulated draft-vs-posted learning
  series (§5). Exists nowhere else; written once per cycle, never pruned.

Credentials: the push to `claude/state` uses the **clone's own git credentials**,
which is a *separate* path from the read-only `GITHUB_TOKEN_<ORG>` API PATs in
§1. Those PATs stay read-only — they are never used to push. (If your routine
host can't push with ambient git creds, give that push path a token scoped to
**Contents: Write on this repo only** — never the org-wide API PATs.)

`state/` is gitignored on `main`, and per §9 it must **never** be shared — it's
your personal review history and learning logs.

## 5. The morning

The night run leaves rounds assembled and **zero GitHub writes**. Open the
session, read the three lanes, and co-sign each write individually — that
co-sign, under your identity, is the whole point.
