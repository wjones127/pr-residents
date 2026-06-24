"""state_sync tests: the pure prune policy (unit) and the git round-trip
(integration against a throwaway repo + bare remote)."""

from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "scripts"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "pr-sync", "scripts"))

import state_sync  # noqa: E402
import store as store_mod  # noqa: E402


class TestPrunePolicy(unittest.TestCase):
    def test_parse_workup_key(self):
        self.assertEqual(
            state_sync._parse_workup_key("cache/workups/owner/name/7416/abc.json"),
            ("owner/name", 7416))
        self.assertIsNone(state_sync._parse_workup_key("ledger/x.json"))
        self.assertIsNone(state_sync._parse_workup_key("cache/records.json"))

    def test_prunes_only_inactive_prs(self):
        keys = [
            "cache/workups/o/n/1/aaa.json",   # active, keep
            "cache/workups/o/n/1/bbb.json",   # active (other SHA), keep
            "cache/workups/o/n/2/ccc.json",   # inactive, prune
        ]
        active = {("o/n", 1)}
        self.assertEqual(state_sync.workups_to_prune(keys, active),
                         ["cache/workups/o/n/2/ccc.json"])

    def test_prune_cache_noops_without_anchor(self):
        # No records/panel -> can't tell what's active -> prune nothing.
        tmp = tempfile.mkdtemp()
        s = store_mod.FileStore(tmp)
        s.put_json("cache/workups/o/n/9/x.json", {"soap": "z"})
        self.assertEqual(state_sync.prune_cache(s), 0)
        self.assertTrue(s.exists("cache/workups/o/n/9/x.json"))

    def test_prune_cache_drops_inactive_keeps_active(self):
        tmp = tempfile.mkdtemp()
        s = store_mod.FileStore(tmp)
        s.put_json(store_mod.RECORDS, [{"repo": "o/n", "number": 1}])
        s.put_json(store_mod.PANEL, [])
        s.put_json("cache/workups/o/n/1/keep.json", {"soap": "a"})
        s.put_json("cache/workups/o/n/2/drop.json", {"soap": "b"})
        self.assertEqual(state_sync.prune_cache(s), 1)
        self.assertTrue(s.exists("cache/workups/o/n/1/keep.json"))
        self.assertFalse(s.exists("cache/workups/o/n/2/drop.json"))


def _git(repo, *args):
    subprocess.run(["git", "-C", repo, *args], check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


class TestGitRoundTrip(unittest.TestCase):
    """persist on run A -> hydrate on a fresh clone B restores the tree. This is
    the property the whole remote routine depends on."""

    def setUp(self):
        self.base = tempfile.mkdtemp()
        self.bare = os.path.join(self.base, "remote.git")
        subprocess.run(["git", "init", "--bare", self.bare], check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        # Working clone A with an initial commit on main.
        self.repo_a = os.path.join(self.base, "a")
        _git(self.base, "clone", self.bare, "a")
        _git(self.repo_a, "config", "user.email", "t@t")
        _git(self.repo_a, "config", "user.name", "t")
        with open(os.path.join(self.repo_a, "README"), "w") as fh:
            fh.write("x")
        _git(self.repo_a, "add", "README")
        _git(self.repo_a, "commit", "-m", "init")
        _git(self.repo_a, "push", "origin", "HEAD:refs/heads/main")

    def test_persist_then_hydrate_fresh_clone(self):
        # Run A writes state and persists it.
        state_a = os.path.join(self.repo_a, "state")
        sa = store_mod.FileStore(state_a)
        sa.put_json(store_mod.RECONCILE_AGREEMENT, {"domains": {"rust/lance": {"samples": 3}}})
        sa.put_json(store_mod.disposition_key("2026-06-23T06:00:00Z"), {"cycle": "c1"})
        sa.put_json("cache/workups/o/n/1/sha.json", {"soap": "review"})
        result = state_sync.persist(self.repo_a, state_a, "cycle 1")
        self.assertTrue(result["committed"])

        # Fresh clone B (simulates the next remote run) hydrates.
        repo_b = os.path.join(self.base, "b")
        _git(self.base, "clone", self.bare, "b")
        state_b = os.path.join(repo_b, "state")
        restored = state_sync.hydrate(repo_b, state_b)
        self.assertTrue(restored)

        sb = store_mod.FileStore(state_b)
        self.assertEqual(
            sb.get_json(store_mod.RECONCILE_AGREEMENT),
            {"domains": {"rust/lance": {"samples": 3}}})
        self.assertEqual(sb.get_json(store_mod.disposition_key("2026-06-23T06:00:00Z")),
                         {"cycle": "c1"})
        self.assertEqual(sb.get_json("cache/workups/o/n/1/sha.json"), {"soap": "review"})

    def test_hydrate_first_run_no_branch(self):
        repo_b = os.path.join(self.base, "b2")
        _git(self.base, "clone", self.bare, "b2")
        state_b = os.path.join(repo_b, "state")
        self.assertFalse(state_sync.hydrate(repo_b, state_b))

    def test_persist_keeps_history_across_two_cycles(self):
        state_a = os.path.join(self.repo_a, "state")
        sa = store_mod.FileStore(state_a)
        sa.put_json(store_mod.disposition_key("c1"), {"cycle": "c1"})
        r1 = state_sync.persist(self.repo_a, state_a, "cycle 1")
        sa.put_json(store_mod.disposition_key("c2"), {"cycle": "c2"})
        r2 = state_sync.persist(self.repo_a, state_a, "cycle 2")
        self.assertEqual(r2["parent"], r1["commit"])


if __name__ == "__main__":
    unittest.main()
