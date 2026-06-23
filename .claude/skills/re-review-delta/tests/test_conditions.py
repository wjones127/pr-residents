"""Tests for conditions-ledger reconstruction. Run:
    python3 .claude/skills/re-review-delta/tests/test_conditions.py
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "scripts"))

import conditions  # noqa: E402

VIEWER = "wjones127"


def thread(body, author=VIEWER, resolved=False, outdated=False, path="a.rs", line=10, tid="t1"):
    return {
        "id": tid, "isResolved": resolved, "isOutdated": outdated,
        "path": path, "line": line, "originalLine": line,
        "comments": {"nodes": [{"author": {"login": author}, "body": body}]},
    }


class TestParseLabel(unittest.TestCase):
    def test_blocking(self):
        self.assertEqual(conditions.parse_label("issue(blocking): foo"), ("issue", "blocking"))

    def test_non_blocking(self):
        self.assertEqual(conditions.parse_label("issue(non-blocking): foo"),
                         ("issue", "non-blocking"))

    def test_suggestion_no_decoration(self):
        self.assertEqual(conditions.parse_label("suggestion: extract a helper"),
                         ("suggestion", None))

    def test_real_examples(self):
        # Verbatim from lance review history.
        self.assertEqual(
            conditions.parse_label("issue(blocking): This seems unnecessary. If we label the X")[0],
            "issue")
        self.assertEqual(
            conditions.parse_label("nitpick: \"non-noop\" is a bit confusing term.")[0], "nitpick")

    def test_markdown_bold(self):
        self.assertEqual(conditions.parse_label("**issue (blocking):** foo"), ("issue", "blocking"))

    def test_unlabeled(self):
        self.assertIsNone(conditions.parse_label("This looks fine to me."))


class TestClassifyKind(unittest.TestCase):
    def test_kinds(self):
        self.assertEqual(conditions.classify_kind("issue", "blocking"), "blocking")
        self.assertEqual(conditions.classify_kind("issue", "non-blocking"), "non_blocking")
        self.assertEqual(conditions.classify_kind("issue", None), "blocking")
        self.assertEqual(conditions.classify_kind("suggestion", None), "suggestion")
        self.assertIsNone(conditions.classify_kind("nitpick", None))
        self.assertIsNone(conditions.classify_kind("question", None))


class TestReconstruct(unittest.TestCase):
    def test_only_my_conditions(self):
        threads = [
            thread("issue(blocking): fix the lock order", tid="a"),
            thread("suggestion: rename this", tid="b"),
            thread("nitpick: spelling", tid="c"),                     # dropped
            thread("issue(blocking): theirs", author="someone", tid="d"),  # not mine
            thread("just a plain comment", tid="e"),                  # unlabeled
        ]
        led = conditions.reconstruct(threads, VIEWER)
        kinds = [c["kind"] for c in led]
        self.assertEqual(kinds, ["blocking", "suggestion"])
        self.assertEqual(led[0]["text"], "fix the lock order")
        self.assertEqual(led[0]["status"], "open")

    def test_resolved_is_recorded_as_claim(self):
        led = conditions.reconstruct(
            [thread("issue(blocking): x", resolved=True, outdated=True)], VIEWER)
        self.assertTrue(led[0]["author_resolved"])
        self.assertTrue(led[0]["is_outdated"])
        # ...but status is NOT met — resolved is only a claim (trap #3).
        self.assertEqual(led[0]["status"], "open")

    def test_location(self):
        led = conditions.reconstruct(
            [thread("issue(blocking): x", path="rust/src/foo.rs", line=42)], VIEWER)
        self.assertEqual(led[0]["location"], "rust/src/foo.rs:42")


if __name__ == "__main__":
    unittest.main()
