"""Unit tests for the pure pr-relevance scoring. Run:
    python3 .claude/skills/pr-relevance/tests/test_affinity.py
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "scripts"))

import affinity  # noqa: E402


class TestBucketPath(unittest.TestCase):
    def test_crate_level(self):
        self.assertEqual(affinity.bucket_path("rust/lance-encoding/src/x.rs"),
                         "rust/lance-encoding")

    def test_python_tree(self):
        self.assertEqual(affinity.bucket_path("python/python/lance/arrow.py"),
                         "python/python")

    def test_root_file_buckets_by_dir_or_self(self):
        self.assertEqual(affinity.bucket_path("README.md"), "README.md")
        self.assertEqual(affinity.bucket_path("docs/guide.md"), "docs")

    def test_empty(self):
        self.assertEqual(affinity.bucket_path(""), "")


class TestBuildProfile(unittest.TestCase):
    def test_counts_prs_not_files(self):
        # One PR touching two files in the same crate counts that crate ONCE.
        reviewed = [
            ["rust/lance-encoding/a.rs", "rust/lance-encoding/b.rs"],
            ["rust/lance-encoding/c.rs", "rust/lance-index/d.rs"],
            ["python/python/lance/x.py"],
        ]
        prof = affinity.build_profile(reviewed)
        self.assertEqual(prof["rust/lance-encoding"], 2)
        self.assertEqual(prof["rust/lance-index"], 1)
        self.assertEqual(prof["python/python"], 1)

    def test_empty_history(self):
        self.assertEqual(affinity.build_profile([]), {})


class TestScoreCandidate(unittest.TestCase):
    def setUp(self):
        self.profile = {"rust/lance-encoding": 14, "rust/lance-index": 6, "python/python": 2}

    def test_sums_overlap_weights(self):
        score, matched = affinity.score_candidate(
            ["rust/lance-encoding/src/x.rs", "rust/lance-index/y.rs"], self.profile)
        self.assertEqual(score, 20.0)
        self.assertEqual(matched, [("rust/lance-encoding", 14), ("rust/lance-index", 6)])

    def test_no_overlap_scores_zero(self):
        score, matched = affinity.score_candidate(["java/Foo.java"], self.profile)
        self.assertEqual(score, 0.0)
        self.assertEqual(matched, [])

    def test_matched_sorted_by_weight_desc(self):
        _, matched = affinity.score_candidate(
            ["python/python/x.py", "rust/lance-encoding/y.rs"], self.profile)
        self.assertEqual([b for b, _ in matched], ["rust/lance-encoding", "python/python"])

    def test_rationale(self):
        _, matched = affinity.score_candidate(["rust/lance-encoding/x.rs"], self.profile)
        self.assertIn("rust/lance-encoding (reviewed 14×)", affinity.affinity_rationale(matched))
        self.assertEqual(affinity.affinity_rationale([]), "no overlap with areas you've reviewed")


class TestColdStart(unittest.TestCase):
    def test_interest_prefix_match(self):
        score, why = affinity.cold_start_score(
            ["rust/lance-encoding/src/x.rs", "docs/y.md"],
            interests=["rust/lance-encoding"], escalation_rule_ids=[])
        self.assertEqual(score, 1.0)
        self.assertIn("matches your interests: rust/lance-encoding", why)
        self.assertIn("cold-start", why)

    def test_interest_is_prefix_not_substring(self):
        # "rust/lance" must not match "rust/lance-encoding" as a bare substring;
        # it matches only on a path-segment boundary.
        score, _ = affinity.cold_start_score(
            ["rust/lance-encoding/x.rs"], interests=["rust/lance-enc"],
            escalation_rule_ids=[])
        self.assertEqual(score, 0.0)

    def test_escalation_adds_to_score(self):
        score, why = affinity.cold_start_score(
            ["protos/format.proto"], interests=[], escalation_rule_ids=["public-api"])
        self.assertEqual(score, 1.0)
        self.assertIn("escalation paths: public-api", why)

    def test_no_signal(self):
        score, why = affinity.cold_start_score(["x/y.rs"], [], [])
        self.assertEqual(score, 0.0)
        self.assertIn("no shared-signal match", why)


if __name__ == "__main__":
    unittest.main()
