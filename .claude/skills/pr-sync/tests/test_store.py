"""Tests for the Store seam: key validation, the cache/ledger split, round-trip,
listing, and the artifact key registry."""

from __future__ import annotations

import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "scripts"))

import store  # noqa: E402
from store import FileStore  # noqa: E402


class TestKeyValidation(unittest.TestCase):
    def setUp(self):
        self.s = FileStore("/tmp/does-not-matter")

    def test_rejects_keys_outside_namespaces(self):
        for bad in ("records.json", "state/x.json", "foo/bar", "/cache/x", "cache\\x"):
            with self.assertRaises(ValueError):
                self.s.local_path(bad)

    def test_rejects_parent_traversal(self):
        with self.assertRaises(ValueError):
            self.s.local_path("cache/../ledger/x.json")

    def test_accepts_cache_and_ledger(self):
        self.assertTrue(self.s.local_path("cache/records.json").endswith("records.json"))
        self.assertTrue(self.s.local_path("ledger/reconcile/agreement.json"))


class TestRoundTrip(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.s = FileStore(self.tmp)

    def test_json_round_trip_creates_parents(self):
        self.assertIsNone(self.s.get_json("cache/records.json"))
        self.s.put_json("cache/workups/o/n/7/abc.json", {"soap": "ok"})
        self.assertEqual(self.s.get_json("cache/workups/o/n/7/abc.json"), {"soap": "ok"})

    def test_exists(self):
        self.assertFalse(self.s.exists("ledger/reconcile/agreement.json"))
        self.s.put_json("ledger/reconcile/agreement.json", {"domains": {}})
        self.assertTrue(self.s.exists("ledger/reconcile/agreement.json"))

    def test_get_json_tolerates_corrupt_file(self):
        self.s.put_text("cache/records.json", "{not json")
        self.assertIsNone(self.s.get_json("cache/records.json"))

    def test_delete(self):
        self.s.put_json("cache/panel.json", [])
        self.assertTrue(self.s.delete("cache/panel.json"))
        self.assertFalse(self.s.delete("cache/panel.json"))

    def test_local_path_is_under_root(self):
        p = self.s.local_path("cache/pr_detail.sqlite")
        self.assertTrue(p.startswith(self.tmp))


class TestListKeys(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.s = FileStore(self.tmp)

    def test_empty_prefix_returns_nothing(self):
        self.assertEqual(self.s.list_keys("ledger/dispositions"), [])

    def test_lists_recursively_and_sorted(self):
        self.s.put_json("ledger/dispositions/2026-06-22.json", {})
        self.s.put_json("ledger/dispositions/2026-06-23.json", {})
        self.s.put_json("cache/records.json", [])  # different namespace, excluded
        keys = self.s.list_keys("ledger/dispositions")
        self.assertEqual(keys, [
            "ledger/dispositions/2026-06-22.json",
            "ledger/dispositions/2026-06-23.json",
        ])

    def test_workups_nested(self):
        self.s.put_json("cache/workups/o/n/7/aaa.json", {})
        self.s.put_json("cache/workups/o/n/8/bbb.json", {})
        self.assertEqual(len(self.s.list_keys("cache/workups")), 2)


class TestArtifactKeys(unittest.TestCase):
    def test_namespaces(self):
        self.assertTrue(store.RECORDS.startswith("cache/"))
        self.assertTrue(store.PANEL.startswith("cache/"))
        self.assertTrue(store.RELEVANCE_PROFILE.startswith("cache/"))
        self.assertTrue(store.PR_DETAIL_DB.startswith("cache/"))
        self.assertTrue(store.RECONCILE_AGREEMENT.startswith("ledger/"))
        self.assertTrue(store.DISPOSITIONS_PREFIX.startswith("ledger/"))

    def test_disposition_key_sanitizes_cycle(self):
        k = store.disposition_key("2026-06-23T06:00:00Z")
        self.assertTrue(k.startswith("ledger/dispositions/"))
        self.assertTrue(k.endswith(".json"))
        self.assertNotIn(":", k)

    def test_workup_key_is_sha_scoped_and_in_cache(self):
        k = store.workup_key("owner/name", 7416, "deadbeef")
        self.assertEqual(k, "cache/workups/owner/name/7416/deadbeef.json")

    def test_workup_key_distinct_per_sha(self):
        a = store.workup_key("o/n", 1, "sha-a")
        b = store.workup_key("o/n", 1, "sha-b")
        self.assertNotEqual(a, b)


if __name__ == "__main__":
    unittest.main()
