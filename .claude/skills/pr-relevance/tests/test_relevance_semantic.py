"""The semantic blend in _score_repo, exercised with a pre-seeded vector cache
so no Ollama daemon is needed. Confirms: semantic off == today's behavior, and
semantic on adds weight*cosine on top of the base score."""

from __future__ import annotations

import os
import sys
import tempfile
import types
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "scripts"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "pr-sync", "scripts"))

import embed  # noqa: E402
import relevance  # noqa: E402
import store as store_mod  # noqa: E402


def _cfg():
    # Cold-start path (reviews=0) with no interests/escalation → base score 0,
    # so the semantic term is the whole score and easy to assert on.
    return types.SimpleNamespace(exclude_paths=[], interests=[], escalation={})


# A non-nomic model name so _task_prefix is empty and seeded keys match exactly.
_MODEL = "test-model"


def _seed(store, title, vec):
    key = store_mod.embedding_key(_MODEL, embed._content_hash(title, _MODEL))
    store.put_json(key, {"model": _MODEL, "v": vec})


class TestSemanticBlend(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.store = store_mod.FileStore(self.tmp)
        self.cand = [{"repo": "o/n", "number": 1, "title": "vector index pruning",
                      "url": "u", "author": "a", "paths": ["rust/lance/x.rs"]}]

    def test_off_matches_base(self):
        prof = {"reviews": 0, "weights": {}}
        panel = relevance._score_repo(self.cand, prof, _cfg())
        self.assertEqual(panel[0]["score"], 0.0)
        self.assertEqual(panel[0]["semantic"], 0.0)

    def test_on_adds_weighted_cosine(self):
        # Candidate vector identical to the centroid → cosine 1.0 → +weight.
        _seed(self.store, "vector index pruning", [1.0, 0.0, 0.0])
        prof = {"reviews": 0, "weights": {}, "centroid": [1.0, 0.0, 0.0]}
        panel = relevance._score_repo(
            self.cand, prof, _cfg(), store=self.store, semantic=True, semantic_weight=5.0, model=_MODEL)
        self.assertAlmostEqual(panel[0]["semantic"], 1.0)
        self.assertAlmostEqual(panel[0]["score"], 5.0)

    def test_on_without_centroid_is_noop(self):
        _seed(self.store, "vector index pruning", [1.0, 0.0, 0.0])
        prof = {"reviews": 0, "weights": {}}  # no centroid
        panel = relevance._score_repo(
            self.cand, prof, _cfg(), store=self.store, semantic=True, semantic_weight=5.0, model=_MODEL)
        self.assertEqual(panel[0]["semantic"], 0.0)
        self.assertEqual(panel[0]["score"], 0.0)

    def test_orthogonal_centroid_adds_nothing(self):
        _seed(self.store, "vector index pruning", [1.0, 0.0, 0.0])
        prof = {"reviews": 0, "weights": {}, "centroid": [0.0, 1.0, 0.0]}
        panel = relevance._score_repo(
            self.cand, prof, _cfg(), store=self.store, semantic=True, semantic_weight=5.0, model=_MODEL)
        self.assertAlmostEqual(panel[0]["semantic"], 0.0)
        self.assertAlmostEqual(panel[0]["score"], 0.0)


if __name__ == "__main__":
    unittest.main()
