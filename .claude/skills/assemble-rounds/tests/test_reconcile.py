"""Tests for the reconciliation semantics (§5). Run:
    python3 .claude/skills/assemble-rounds/tests/test_reconcile.py
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "scripts"))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "pr-sync", "scripts"))

import reconcile  # noqa: E402
import store as store_mod  # noqa: E402


class TestClassifyOutcome(unittest.TestCase):
    def test_approve_agrees(self):
        self.assertEqual(reconcile.classify_outcome("approve", "APPROVED"), "agree")

    def test_block_agrees(self):
        self.assertEqual(
            reconcile.classify_outcome("block on 2 conditions", "CHANGES_REQUESTED"), "agree")

    def test_over_call_blocked_but_i_approved(self):
        # Resident drafted block; I approved -> it was too strict.
        self.assertEqual(reconcile.classify_outcome("block", "APPROVED"), "over_call")

    def test_under_call_approved_but_i_blocked(self):
        # Resident drafted approve; I requested changes -> it missed a blocker.
        self.assertEqual(reconcile.classify_outcome("approve", "CHANGES_REQUESTED"), "under_call")

    def test_commented_is_partial(self):
        self.assertEqual(reconcile.classify_outcome("approve", "COMMENTED"), "partial")
        self.assertEqual(reconcile.classify_outcome("block", "COMMENTED"), "partial")

    def test_no_post_is_no_action(self):
        self.assertEqual(reconcile.classify_outcome("approve", None), "no_action")

    def test_non_decision_draft_is_partial(self):
        self.assertEqual(reconcile.classify_outcome("needs a sitting", "APPROVED"), "partial")


class TestUpdateAgreement(unittest.TestCase):
    def test_accumulates(self):
        s = None
        for outcome in ("agree", "agree", "over_call", "partial"):
            s = reconcile.update_agreement(s, outcome)
        self.assertEqual(s["samples"], 4)
        self.assertEqual(s["agree"], 2)
        self.assertEqual(s["over_call"], 1)
        self.assertEqual(s["partial"], 1)

    def test_no_action_does_not_count(self):
        s = reconcile.update_agreement(None, "no_action")
        self.assertEqual(s["samples"], 0)

    def test_pure_does_not_mutate_input(self):
        s0 = dict(reconcile._EMPTY)
        reconcile.update_agreement(s0, "agree")
        self.assertEqual(s0["samples"], 0)  # input untouched


class TestAgreementRate(unittest.TestCase):
    def test_none_when_empty(self):
        self.assertIsNone(reconcile.agreement_rate({"samples": 0}))

    def test_rate(self):
        self.assertEqual(
            reconcile.agreement_rate({"samples": 4, "agree": 3}), 0.75)


class TestAnchoringFlag(unittest.TestCase):
    def test_not_flagged_below_min_samples(self):
        stats = {"samples": 5, "agree": 5, "over_call": 0, "under_call": 0}
        self.assertFalse(reconcile.anchoring_flag(stats, min_samples=8))

    def test_flagged_when_dissent_flatlines(self):
        # Plenty of samples, never once disagreed -> suspicious (§5).
        stats = {"samples": 10, "agree": 8, "partial": 2, "over_call": 0, "under_call": 0}
        self.assertTrue(reconcile.anchoring_flag(stats, min_samples=8))

    def test_not_flagged_when_dissent_present(self):
        stats = {"samples": 10, "agree": 7, "over_call": 1, "under_call": 2}
        self.assertFalse(reconcile.anchoring_flag(stats, min_samples=8))


class TestFindUnreconciled(unittest.TestCase):
    def test_skips_already_reconciled(self):
        import tempfile
        with tempfile.TemporaryDirectory() as tmp:
            store = store_mod.FileStore(tmp)
            for cyc in ("2026-06-20T00:00:00Z", "2026-06-21T00:00:00Z"):
                store.put_json(store_mod.disposition_key(cyc),
                               {"cycle": cyc, "dispositions": []})
            pending = reconcile.find_unreconciled(store, {"2026-06-20T00:00:00Z"})
            self.assertEqual([c["cycle"] for c in pending], ["2026-06-21T00:00:00Z"])


if __name__ == "__main__":
    unittest.main()
