"""Tests for embed.py — the pure math (cosine, centroid, hashing) and the
cache round-trip. The Ollama HTTP call is exercised by a daemon-gated live check,
not here, so this suite runs with no model installed."""

from __future__ import annotations

import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "scripts"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "pr-sync", "scripts"))

import embed  # noqa: E402
import store as store_mod  # noqa: E402


class TestCosine(unittest.TestCase):
    def test_identical_is_one(self):
        self.assertAlmostEqual(embed.cosine([1.0, 2.0, 3.0], [1.0, 2.0, 3.0]), 1.0)

    def test_orthogonal_is_zero(self):
        self.assertAlmostEqual(embed.cosine([1.0, 0.0], [0.0, 1.0]), 0.0)

    def test_opposite_is_minus_one(self):
        self.assertAlmostEqual(embed.cosine([1.0, 1.0], [-1.0, -1.0]), -1.0)

    def test_scale_invariant(self):
        self.assertAlmostEqual(embed.cosine([1.0, 2.0], [2.0, 4.0]), 1.0)

    def test_degenerate_inputs_are_zero(self):
        self.assertEqual(embed.cosine([], [1.0]), 0.0)
        self.assertEqual(embed.cosine([0.0, 0.0], [1.0, 1.0]), 0.0)
        self.assertEqual(embed.cosine([1.0, 2.0], [1.0]), 0.0)  # length mismatch


class TestCentroid(unittest.TestCase):
    def test_mean(self):
        self.assertEqual(embed.centroid([[0.0, 0.0], [2.0, 4.0]]), [1.0, 2.0])

    def test_single(self):
        self.assertEqual(embed.centroid([[1.0, 2.0, 3.0]]), [1.0, 2.0, 3.0])

    def test_empty_is_none(self):
        self.assertIsNone(embed.centroid([]))
        self.assertIsNone(embed.centroid([None, []]))

    def test_ragged_is_none(self):
        self.assertIsNone(embed.centroid([[1.0, 2.0], [1.0]]))


class TestContentHash(unittest.TestCase):
    def test_stable(self):
        self.assertEqual(embed._content_hash("hi", "m"), embed._content_hash("hi", "m"))

    def test_model_scoped(self):
        self.assertNotEqual(embed._content_hash("hi", "a"), embed._content_hash("hi", "b"))

    def test_text_scoped(self):
        self.assertNotEqual(embed._content_hash("hi", "m"), embed._content_hash("bye", "m"))


class TestEmbedCached(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.store = store_mod.FileStore(self.tmp)

    def test_serves_from_cache_without_daemon(self):
        # Pre-seed the cache; embed_cached must return it without any network call.
        key = store_mod.embedding_key(embed.DEFAULT_MODEL,
                                      embed._content_hash("hello", embed.DEFAULT_MODEL))
        self.store.put_json(key, {"model": embed.DEFAULT_MODEL, "v": [0.1, 0.2, 0.3]})
        got = embed.embed_cached(self.store, "hello",
                                 host="http://127.0.0.1:1")  # bogus host, must not be hit
        self.assertEqual(got, [0.1, 0.2, 0.3])

    def test_miss_with_unreachable_daemon_returns_none(self):
        got = embed.embed_cached(self.store, "uncached", host="http://127.0.0.1:1")
        self.assertIsNone(got)

    def test_cache_lands_in_cache_namespace(self):
        key = store_mod.embedding_key("m", "h")
        self.assertTrue(key.startswith("cache/embeddings/"))


class TestAvailable(unittest.TestCase):
    def test_unreachable_host_is_false(self):
        self.assertFalse(embed.available("http://127.0.0.1:1", timeout=0.5))


if __name__ == "__main__":
    unittest.main()
