"""Tests for the workup cache: SHA-scoped hit/miss and round-trip."""

from __future__ import annotations

import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "scripts"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "pr-sync", "scripts"))

import store as store_mod  # noqa: E402
import workup  # noqa: E402


class TestWorkupCache(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.store = store_mod.FileStore(self.tmp)

    def test_miss_then_hit(self):
        self.assertIsNone(workup.get_workup(self.store, "o/n", 7, "sha1"))
        workup.put_workup(self.store, "o/n", 7, "sha1", "SOAP body", "2026-06-23T06:00:00Z")
        hit = workup.get_workup(self.store, "o/n", 7, "sha1")
        self.assertEqual(hit["soap"], "SOAP body")
        self.assertEqual(hit["cached_at"], "2026-06-23T06:00:00Z")

    def test_force_push_invalidates(self):
        workup.put_workup(self.store, "o/n", 7, "old-sha", "stale review")
        # PR force-pushed -> new head SHA -> cache must miss, not serve stale.
        self.assertIsNone(workup.get_workup(self.store, "o/n", 7, "new-sha"))

    def test_workup_lands_in_cache_namespace(self):
        workup.put_workup(self.store, "o/n", 7, "sha1", "body")
        keys = self.store.list_keys("cache/workups")
        self.assertEqual(keys, ["cache/workups/o/n/7/sha1.json"])

    def test_cli_get_miss_returns_exit_3(self):
        rc = workup.main(["get", "--repo", "o/n", "--number", "7", "--sha", "x",
                          "--state-dir", self.tmp])
        self.assertEqual(rc, workup.MISS)


if __name__ == "__main__":
    unittest.main()
