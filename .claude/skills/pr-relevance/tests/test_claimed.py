"""is_claimed: a relevance candidate is dropped once another human has reviewed
it. Bots, my own reviews, and dismissed/pending reviews don't count."""

from __future__ import annotations

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "scripts"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "pr-sync", "scripts"))

import relevance  # noqa: E402

ME = "wjones127"


def _rev(login, state="APPROVED", typ="User"):
    return {"login": login, "state": state, "type": typ}


class TestIsClaimed(unittest.TestCase):
    def test_no_reviews_unclaimed(self):
        self.assertFalse(relevance.is_claimed([], ME))
        self.assertFalse(relevance.is_claimed(None, ME))

    def test_other_human_claims(self):
        self.assertTrue(relevance.is_claimed([_rev("alice")], ME))

    def test_other_human_commented_still_claims(self):
        self.assertTrue(relevance.is_claimed([_rev("alice", state="COMMENTED")], ME))
        self.assertTrue(relevance.is_claimed([_rev("alice", state="CHANGES_REQUESTED")], ME))

    def test_my_own_review_does_not_claim(self):
        self.assertFalse(relevance.is_claimed([_rev(ME)], ME))

    def test_bot_by_typename_does_not_claim(self):
        self.assertFalse(relevance.is_claimed([_rev("some-ci", typ="Bot")], ME))

    def test_bot_by_login_suffix_does_not_claim(self):
        self.assertFalse(relevance.is_claimed([_rev("coderabbitai[bot]", typ="User")], ME))
        self.assertFalse(relevance.is_claimed([_rev("dependabot[bot]")], ME))

    def test_dismissed_and_pending_do_not_claim(self):
        self.assertFalse(relevance.is_claimed([_rev("alice", state="DISMISSED")], ME))
        self.assertFalse(relevance.is_claimed([_rev("alice", state="PENDING")], ME))

    def test_mixed_bot_and_human(self):
        revs = [_rev("ci-bot", typ="Bot"), _rev("alice")]
        self.assertTrue(relevance.is_claimed(revs, ME))

    def test_only_bots_and_me_unclaimed(self):
        revs = [_rev("ci-bot", typ="Bot"), _rev(ME), _rev("x[bot]")]
        self.assertFalse(relevance.is_claimed(revs, ME))

    def test_null_author_skipped(self):
        # Deleted reviewer account → author null → not a claim signal.
        self.assertFalse(relevance.is_claimed([{"login": None, "state": "APPROVED"}], ME))


if __name__ == "__main__":
    unittest.main()
