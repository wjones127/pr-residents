"""Unit tests for the pure PRRecord derivations. Run:
    cd .claude/skills/pr-sync && python3 -m unittest discover tests
"""

import os
import sys
import unittest
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "scripts"))

import derive  # noqa: E402

NOW = datetime(2026, 6, 23, 12, 0, 0, tzinfo=timezone.utc)
HEAD = "aaa111"
OLD = "bbb222"

ESCALATION = {
    "path_rules": [
        {"id": "secrets", "reason": "secret", "any_path_matches": ["**/*secret*", "**/.env*"]},
        {"id": "public-api", "reason": "public api", "any_path_matches": ["**/*.proto"]},
    ],
    "label_rules": [
        {"id": "breaking", "reason": "breaking", "any_label_matches": ["breaking-change"]},
    ],
    "size_rules": [
        {"id": "xl", "reason": "xl", "min_total_lines": 1000},
    ],
}


def hours_ago(h):
    return (NOW - timedelta(hours=h)).isoformat().replace("+00:00", "Z")


def detail(**kw):
    base = {
        "number": 1, "url": "u", "title": "t", "author": {"login": "alice"},
        "headRefOid": HEAD, "reviewDecision": None, "mergeable": "MERGEABLE",
        "additions": 20, "deletions": 5, "changedFiles": 2,
        "labels": {"nodes": []},
        "files": {"nodes": [{"path": "rust/src/foo.rs"}]},
        "reviewRequests": {"nodes": []},
        "reviews": {"nodes": []},
        "commits": {"nodes": [{"commit": {"oid": HEAD, "committedDate": hours_ago(2),
                                          "statusCheckRollup": {"state": "SUCCESS"}}}]},
        "timelineItems": {"nodes": []},
    }
    base.update(kw)
    return base


def review(state, oid, hrs, who="wjones127"):
    return {"author": {"login": who}, "state": state, "submittedAt": hours_ago(hrs),
            "commit": {"oid": oid}}


class TestGlob(unittest.TestCase):
    def test_double_star_prefix(self):
        self.assertTrue(derive.path_matches("a/b/secret.rs", "**/*secret*"))
        self.assertTrue(derive.path_matches("secret.txt", "**/*secret*"))
        self.assertFalse(derive.path_matches("safe.rs", "**/*secret*"))

    def test_single_star_segment(self):
        self.assertTrue(derive.path_matches("a/x.proto", "**/*.proto"))
        self.assertFalse(derive.path_matches("a/x.protobuf", "**/*.proto"))


class TestEscalation(unittest.TestCase):
    def test_path_rule(self):
        e = derive.match_escalation(["src/secret_store.rs"], [], 10, ESCALATION)
        self.assertTrue(e["forced"])
        self.assertIn("secrets", e["rule_ids"])

    def test_label_rule(self):
        e = derive.match_escalation(["a.rs"], ["breaking-change"], 10, ESCALATION)
        self.assertTrue(e["forced"])
        self.assertIn("breaking", e["rule_ids"])

    def test_size_rule(self):
        e = derive.match_escalation(["a.rs"], [], 1500, ESCALATION)
        self.assertIn("xl", e["rule_ids"])

    def test_no_match(self):
        e = derive.match_escalation(["a.rs"], [], 10, ESCALATION)
        self.assertFalse(e["forced"])


class TestEffort(unittest.TestCase):
    def test_buckets(self):
        self.assertEqual(derive.derive_effort(5, 3, 1)["size_bucket"], "XS")
        self.assertEqual(derive.derive_effort(30, 10, 2)["size_bucket"], "S")
        self.assertEqual(derive.derive_effort(150, 40, 5)["size_bucket"], "M")
        self.assertEqual(derive.derive_effort(400, 150, 9)["size_bucket"], "L")
        self.assertEqual(derive.derive_effort(900, 200, 30)["size_bucket"], "XL")


class TestBlockedOn(unittest.TestCase):
    def test_never_reviewed_requested(self):
        bo, _ = derive.derive_blocked_on(detail(), "wjones127", requested=True)
        self.assertEqual(bo, "me")

    def test_never_reviewed_not_requested(self):
        bo, _ = derive.derive_blocked_on(detail(), "wjones127", requested=False)
        self.assertEqual(bo, "other_reviewer")

    def test_approved_current_head_is_merge(self):
        d = detail(reviews={"nodes": [review("APPROVED", HEAD, 5)]})
        bo, _ = derive.derive_blocked_on(d, "wjones127", requested=False)
        self.assertEqual(bo, "merge")

    def test_changes_requested_no_push_is_author(self):
        d = detail(reviews={"nodes": [review("CHANGES_REQUESTED", HEAD, 5)]})
        bo, _ = derive.derive_blocked_on(d, "wjones127", requested=False)
        self.assertEqual(bo, "author")

    def test_author_pushed_since_review_is_me(self):
        # I approved an OLD sha; head moved -> re-review owed (stale approval reopened)
        d = detail(reviews={"nodes": [review("APPROVED", OLD, 5)]})
        bo, _ = derive.derive_blocked_on(d, "wjones127", requested=False)
        self.assertEqual(bo, "me")

    def test_draft_is_author_even_when_requested(self):
        # A draft is the author's court regardless of a pending review request.
        bo, _ = derive.derive_blocked_on(detail(isDraft=True), "wjones127", requested=True)
        self.assertEqual(bo, "author")

    def test_draft_is_author_even_after_my_review(self):
        # I reviewed, then the author reverted to draft -> back to their court,
        # not re_review.
        d = detail(isDraft=True, reviews={"nodes": [review("COMMENTED", OLD, 10)]})
        bo, _ = derive.derive_blocked_on(d, "wjones127", requested=True)
        self.assertEqual(bo, "author")


class TestBuildRecord(unittest.TestCase):
    def test_fresh_lane(self):
        rec = derive.build_record(detail(), "wjones127", True, ESCALATION, now=NOW)
        self.assertEqual(rec["lane"], "fresh")
        self.assertEqual(rec["blocked_on"], "me")
        self.assertIsNone(rec["delta"])

    def test_rereview_lane(self):
        d = detail(reviews={"nodes": [review("COMMENTED", OLD, 10)]})
        rec = derive.build_record(d, "wjones127", True, ESCALATION, now=NOW)
        self.assertEqual(rec["lane"], "re_review")
        self.assertEqual(rec["last_reviewed_sha"], OLD)
        self.assertIsNotNone(rec["delta"])

    def test_housekeeping_merge(self):
        d = detail(reviews={"nodes": [review("APPROVED", HEAD, 5)]})
        rec = derive.build_record(d, "wjones127", False, ESCALATION, now=NOW)
        self.assertEqual(rec["lane"], "housekeeping")
        self.assertEqual(rec["blocked_on"], "merge")

    def test_stale_draft_lands_in_housekeeping_not_rereview(self):
        # Requested + previously reviewed would be re_review — but it's a draft,
        # and it's been one for >48h, so it's stale-waiting-on-author housekeeping.
        d = detail(isDraft=True, reviews={"nodes": [review("COMMENTED", OLD, 72)]},
                   commits={"nodes": [{"commit": {"oid": HEAD, "committedDate": hours_ago(72),
                                                  "statusCheckRollup": {"state": "SUCCESS"}}}]})
        rec = derive.build_record(d, "wjones127", True, ESCALATION, now=NOW)
        self.assertEqual(rec["lane"], "housekeeping")
        self.assertTrue(rec["is_draft"])

    def test_fresh_draft_is_not_surfaced(self):
        # A brand-new draft I'm requested on isn't ready — drop it entirely.
        d = detail(isDraft=True,
                   commits={"nodes": [{"commit": {"oid": HEAD, "committedDate": hours_ago(2),
                                                  "statusCheckRollup": {"state": "SUCCESS"}}}]})
        rec = derive.build_record(d, "wjones127", True, ESCALATION, now=NOW)
        self.assertIsNone(rec)

    def test_own_pr_never_surfaced(self):
        # A PR I authored is never a review candidate, regardless of state.
        d = detail(author={"login": "wjones127"},
                   reviews={"nodes": [review("APPROVED", HEAD, 5)]})
        rec = derive.build_record(d, "wjones127", True, ESCALATION, now=NOW)
        self.assertIsNone(rec)

    def test_other_reviewer_dropped(self):
        rec = derive.build_record(detail(), "wjones127", False, ESCALATION, now=NOW)
        self.assertIsNone(rec)

    def test_escalation_does_not_collapse_rereview(self):
        # Escalated PR with new commits since my review stays in re_review:
        # escalation must NOT drop the conditions ledger for high-stakes PRs.
        d = detail(files={"nodes": [{"path": "proto/format.proto"}]},
                   reviews={"nodes": [review("COMMENTED", OLD, 10)]})
        rec = derive.build_record(d, "wjones127", True, ESCALATION, now=NOW)
        self.assertTrue(rec["escalation"]["forced"])
        self.assertEqual(rec["lane"], "re_review")
        self.assertIsNotNone(rec["delta"])

    def test_escalation_decoupled_from_risk(self):
        # Decoupled (plan §5): a forced escalation does NOT auto-set risk=high.
        # Label-escalated but docs-only change -> forced yet low risk.
        d = detail(files={"nodes": [{"path": "docs/guide.md"}]},
                   labels={"nodes": [{"name": "breaking-change"}]})
        rec = derive.build_record(d, "wjones127", True, ESCALATION, now=NOW)
        self.assertTrue(rec["escalation"]["forced"])
        self.assertEqual(rec["acuity"]["risk"], "low")

    def test_low_risk_docs_only(self):
        d = detail(files={"nodes": [{"path": "docs/guide.md"}, {"path": "README.md"}]})
        rec = derive.build_record(d, "wjones127", True, ESCALATION, now=NOW)
        self.assertEqual(rec["acuity"]["risk"], "low")


def commits(oid=HEAD, hrs=2, ci="SUCCESS"):
    return {"nodes": [{"commit": {"oid": oid, "committedDate": hours_ago(hrs),
                                  "statusCheckRollup": {"state": ci}}}]}


class TestHousekeepingBucket(unittest.TestCase):
    def _rec(self, **kw):
        return derive.build_record(detail(**kw), "wjones127", False, ESCALATION, now=NOW)

    def test_approved_green_mergeable_is_ready(self):
        # Approved by me, CI green, no conflict, no required-review rule (None).
        rec = self._rec(reviews={"nodes": [review("APPROVED", HEAD, 5)]})
        self.assertEqual(rec["lane"], "housekeeping")
        self.assertEqual(derive.housekeeping_bucket(rec), "ready")

    def test_approved_decision_approved_is_ready(self):
        rec = self._rec(reviewDecision="APPROVED",
                        reviews={"nodes": [review("APPROVED", HEAD, 5)]})
        self.assertEqual(derive.housekeeping_bucket(rec), "ready")

    def test_conflict_is_needs_author(self):
        rec = self._rec(mergeable="CONFLICTING",
                        reviews={"nodes": [review("APPROVED", HEAD, 5)]})
        self.assertEqual(derive.housekeeping_bucket(rec), "needs_author")

    def test_red_ci_is_needs_author(self):
        rec = self._rec(commits=commits(ci="FAILURE"),
                        reviews={"nodes": [review("APPROVED", HEAD, 5)]})
        self.assertEqual(derive.housekeeping_bucket(rec), "needs_author")

    def test_pending_ci_is_needs_author(self):
        rec = self._rec(commits=commits(ci="PENDING"),
                        reviews={"nodes": [review("APPROVED", HEAD, 5)]})
        self.assertEqual(derive.housekeeping_bucket(rec), "needs_author")

    def test_review_required_is_needs_author(self):
        # I approved, but branch protection still wants another approval.
        rec = self._rec(reviewDecision="REVIEW_REQUIRED",
                        reviews={"nodes": [review("APPROVED", HEAD, 5)]})
        self.assertEqual(derive.housekeeping_bucket(rec), "needs_author")

    def test_stale_changes_requested_is_stale_author(self):
        rec = self._rec(reviews={"nodes": [review("CHANGES_REQUESTED", HEAD, 72)]})
        self.assertEqual(rec["lane"], "housekeeping")
        self.assertEqual(derive.housekeeping_bucket(rec), "stale_author")

    def test_stale_draft_is_stale_author(self):
        rec = self._rec(isDraft=True,
                        reviews={"nodes": [review("COMMENTED", OLD, 72)]},
                        commits=commits(hrs=72))
        self.assertEqual(derive.housekeeping_bucket(rec), "stale_author")

    def test_record_carries_review_decision(self):
        rec = self._rec(reviewDecision="APPROVED",
                        reviews={"nodes": [review("APPROVED", HEAD, 5)]})
        self.assertEqual(rec["review_decision"], "APPROVED")


class TestAuthorStatus(unittest.TestCase):
    def test_buckets(self):
        self.assertEqual(derive.derive_author_status(0), "first_time")
        self.assertEqual(derive.derive_author_status(1), "infrequent")
        self.assertEqual(derive.derive_author_status(3), "infrequent")
        self.assertEqual(derive.derive_author_status(4), "regular")
        self.assertEqual(derive.derive_author_status(10), "regular")
        self.assertEqual(derive.derive_author_status(11), "core")

    def test_unknown_when_uncounted(self):
        # A failed count must read as 'unknown', never silently first_time.
        self.assertEqual(derive.derive_author_status(None), "unknown")


class TestTriageFields(unittest.TestCase):
    def test_record_carries_triage_inputs(self):
        d = detail(body="Fixes #123", files={"nodes": [{"path": "rust/src/foo.rs"},
                                                        {"path": "docs/x.md"}]})
        rec = derive.build_record(d, "wjones127", True, ESCALATION, now=NOW,
                                  author_merged_count=0)
        self.assertEqual(rec["body"], "Fixes #123")
        self.assertEqual(rec["author_status"], "first_time")
        self.assertEqual(rec["files_changed"], ["rust/src/foo.rs", "docs/x.md"])

    def test_author_status_unknown_without_count(self):
        # build_record default (count not supplied) must not fabricate a status.
        rec = derive.build_record(detail(), "wjones127", True, ESCALATION, now=NOW)
        self.assertEqual(rec["author_status"], "unknown")


class TestRealEscalationConfig(unittest.TestCase):
    """Guard the shared escalation policy against known false positives."""

    @classmethod
    def setUpClass(cls):
        sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "scripts"))
        import miniyaml
        cfg_dir = os.path.join(os.path.dirname(__file__), "..", "..", "..", "..", "config")
        cls.rules = miniyaml.load_file(os.path.join(cfg_dir, "escalation.yml"))

    def test_tokenizer_is_not_a_secret(self):
        # Regression: lance #5583 "tokenizer plugins" wrongly hit `**/*token*`.
        e = derive.match_escalation(["rust/lance-index/src/tokenizer.rs"], [], 10, self.rules)
        self.assertNotIn("secrets", e["rule_ids"])

    def test_real_credential_file_is_a_secret(self):
        e = derive.match_escalation(["python/lance/.env.local"], [], 10, self.rules)
        self.assertIn("secrets", e["rule_ids"])


if __name__ == "__main__":
    unittest.main()
