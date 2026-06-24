"""Local text embeddings for the semantic half of pr-relevance (§8's k-NN).

The pip/stdlib lock that deferred k-NN is routed around here: the embedding model
runs in its *own* process (Ollama, a standalone binary — not a pip package) and
speaks HTTP, so this module only does a urllib POST and some arithmetic. No pip,
no torch, no lancedb. The default model is `nomic-embed-text` (768-dim, long
context, Apache-licensed) — see SKILL.md.

Vectors are GitHub-derived → they ride the Store seam's `cache/` namespace, keyed
by (model, content hash). Embed once locally; the vectors round-trip via
`claude/state`, and the *scoring* (cosine) is pure stdlib that runs anywhere —
so a remote run with no Ollama still ranks on cached vectors and falls back to
path-affinity for anything not yet embedded. Local-first, graceful degrade.

Usage:
    embed.py --check                  # is the daemon up? (exit 0 / 3)
    embed.py "some text"              # print dims + first few components
"""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import sys
import urllib.error
import urllib.request

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(_HERE, "..", "..", "pr-sync", "scripts"))

import store as store_mod  # noqa: E402

DEFAULT_MODEL = "nomic-embed-text"
DEFAULT_HOST = os.environ.get("OLLAMA_HOST", "http://localhost:11434")
_UNREACHABLE = 3


# --- pure math (no network) -----------------------------------------------

def cosine(a, b) -> float:
    """Cosine similarity of two equal-length vectors. 0.0 if either is degenerate."""
    if not a or not b or len(a) != len(b):
        return 0.0
    dot = sum(x * y for x, y in zip(a, b))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(y * y for y in b))
    if na == 0.0 or nb == 0.0:
        return 0.0
    return dot / (na * nb)


def centroid(vectors):
    """Mean vector of a non-empty list of equal-length vectors, or None."""
    vecs = [v for v in vectors if v]
    if not vecs:
        return None
    dim = len(vecs[0])
    if any(len(v) != dim for v in vecs):
        return None
    return [sum(v[i] for v in vecs) / len(vecs) for i in range(dim)]


def _content_hash(text: str, model: str) -> str:
    return hashlib.sha256(f"{model}\x00{text}".encode("utf-8")).hexdigest()[:32]


# --- the embedding call (Ollama over HTTP, stdlib only) -------------------

def available(host: str = DEFAULT_HOST, timeout: float = 1.5) -> bool:
    """Is an Ollama daemon reachable? Cheap GET so callers can degrade quietly."""
    try:
        with urllib.request.urlopen(f"{host}/api/tags", timeout=timeout) as resp:
            return resp.status == 200
    except (urllib.error.URLError, OSError, ValueError):
        return False


def embed_text(text: str, *, model: str = DEFAULT_MODEL, host: str = DEFAULT_HOST,
               timeout: float = 30.0):
    """Embed one string via Ollama. Returns the vector, or None if the daemon is
    unreachable / errors — callers fall back to path-affinity, never crash."""
    payload = json.dumps({"model": model, "prompt": text}).encode("utf-8")
    req = urllib.request.Request(
        f"{host}/api/embeddings", data=payload,
        headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            data = json.loads(resp.read())
    except (urllib.error.URLError, OSError, ValueError) as exc:
        print(f"[embed] {model} unavailable: {exc}", file=sys.stderr)
        return None
    vec = data.get("embedding")
    return vec or None


def embed_cached(store, text: str, *, model: str = DEFAULT_MODEL,
                 host: str = DEFAULT_HOST, prefix: str = ""):
    """embed_text, but memoized through the Store seam (cache/embeddings/...).
    A cache hit needs no daemon, so cached vectors work on a remote run.

    `prefix` is prepended before embedding (nomic-style task instructions like
    "search_query: ") and folded into the cache key, so the document- and
    query-side encodings of the same text cache separately."""
    effective = f"{prefix}{text}"
    key = store_mod.embedding_key(model, _content_hash(effective, model))
    hit = store.get_json(key)
    if hit and hit.get("v"):
        return hit["v"]
    vec = embed_text(effective, model=model, host=host)
    if vec is not None:
        store.put_json(key, {"model": model, "v": vec})
    return vec


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Local embeddings via Ollama (stdlib).")
    parser.add_argument("text", nargs="?", help="text to embed (omit with --check)")
    parser.add_argument("--check", action="store_true", help="probe the daemon and exit")
    parser.add_argument("--model", default=DEFAULT_MODEL)
    parser.add_argument("--host", default=DEFAULT_HOST)
    args = parser.parse_args(argv)

    if args.check:
        up = available(args.host)
        print(f"[embed] {args.host}: {'up' if up else 'unreachable'}", file=sys.stderr)
        return 0 if up else _UNREACHABLE

    if not args.text:
        parser.error("provide text to embed, or use --check")
    vec = embed_text(args.text, model=args.model, host=args.host)
    if vec is None:
        return _UNREACHABLE
    print(f"dims={len(vec)} head={[round(x, 4) for x in vec[:5]]}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
