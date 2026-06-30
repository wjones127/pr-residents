"""The self-requested candidate query: open, review-ready PRs no one routed to
me and I haven't touched. Drafts must be excluded (`draft:false`) — they are the
author saying "not yet"."""

from __future__ import annotations

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "scripts"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "pr-sync", "scripts"))

import relevance  # noqa: E402


class TestCandidateQuery(unittest.TestCase):
    def setUp(self):
        self.q = relevance._candidate_query("lance-format/lance")

    def test_excludes_drafts(self):
        self.assertIn("draft:false", self.q)

    def test_scoped_to_repo(self):
        self.assertIn("repo:lance-format/lance", self.q)

    def test_open_prs_only(self):
        self.assertIn("is:open", self.q)
        self.assertIn("is:pr", self.q)

    def test_excludes_mine_requested_and_reviewed(self):
        self.assertIn("-author:@me", self.q)
        self.assertIn("-review-requested:@me", self.q)
        self.assertIn("-reviewed-by:@me", self.q)


if __name__ == "__main__":
    unittest.main()
