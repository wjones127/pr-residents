"""Tests for conditions-ledger reconstruction. Run:
python3 .claude/skills/re-review-delta/tests/test_conditions.py
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "scripts"))

import conditions  # noqa: E402

VIEWER = "wjones127"


def thread(
    body, author=VIEWER, resolved=False, outdated=False, path="a.rs", line=10, tid="t1"
):
    return {
        "id": tid,
        "isResolved": resolved,
        "isOutdated": outdated,
        "path": path,
        "line": line,
        "originalLine": line,
        "comments": {"nodes": [{"author": {"login": author}, "body": body}]},
    }


class TestParseLabel(unittest.TestCase):
    def test_blocking(self):
        self.assertEqual(
            conditions.parse_label("issue(blocking): foo"), ("issue", "blocking")
        )

    def test_non_blocking(self):
        self.assertEqual(
            conditions.parse_label("issue(non-blocking): foo"),
            ("issue", "non-blocking"),
        )

    def test_suggestion_no_decoration(self):
        self.assertEqual(
            conditions.parse_label("suggestion: extract a helper"), ("suggestion", None)
        )

    def test_real_examples(self):
        # Verbatim from lance review history.
        self.assertEqual(
            conditions.parse_label(
                "issue(blocking): This seems unnecessary. If we label the X"
            )[0],
            "issue",
        )
        self.assertEqual(
            conditions.parse_label('nitpick: "non-noop" is a bit confusing term.')[0],
            "nitpick",
        )

    def test_markdown_bold(self):
        self.assertEqual(
            conditions.parse_label("**issue (blocking):** foo"), ("issue", "blocking")
        )

    def test_unlabeled(self):
        self.assertIsNone(conditions.parse_label("This looks fine to me."))


class TestClassifyKind(unittest.TestCase):
    def test_kinds(self):
        self.assertEqual(conditions.classify_kind("issue", "blocking"), "blocking")
        self.assertEqual(
            conditions.classify_kind("issue", "non-blocking"), "non_blocking"
        )
        self.assertEqual(conditions.classify_kind("issue", None), "blocking")
        self.assertEqual(conditions.classify_kind("suggestion", None), "suggestion")
        self.assertIsNone(conditions.classify_kind("nitpick", None))
        self.assertIsNone(conditions.classify_kind("question", None))


class TestReconstruct(unittest.TestCase):
    def test_only_my_conditions(self):
        threads = [
            thread("issue(blocking): fix the lock order", tid="a"),
            thread("suggestion: rename this", tid="b"),
            thread("nitpick: spelling", tid="c"),  # dropped
            thread("issue(blocking): theirs", author="someone", tid="d"),  # not mine
            thread("just a plain comment", tid="e"),  # unlabeled
        ]
        led = conditions.reconstruct(threads, VIEWER)
        kinds = [c["kind"] for c in led]
        self.assertEqual(kinds, ["blocking", "suggestion"])
        self.assertEqual(led[0]["text"], "fix the lock order")
        self.assertEqual(led[0]["status"], "open")

    def test_resolved_is_recorded_as_claim(self):
        led = conditions.reconstruct(
            [thread("issue(blocking): x", resolved=True, outdated=True)], VIEWER
        )
        self.assertTrue(led[0]["author_resolved"])
        self.assertTrue(led[0]["is_outdated"])
        # ...but status is NOT met — resolved is only a claim (trap #3).
        self.assertEqual(led[0]["status"], "open")

    def test_location(self):
        led = conditions.reconstruct(
            [thread("issue(blocking): x", path="rust/src/foo.rs", line=42)], VIEWER
        )
        self.assertEqual(led[0]["location"], "rust/src/foo.rs:42")


class TestScopeDeltaFiles(unittest.TestCase):
    def test_excludes_off_branch_churn(self):
        import rereview

        compare_files = [
            {"filename": "src/a.rs"},  # author's file
            {"filename": "src/b.rs"},  # author's file
            {"filename": "vendor/main_churn.rs"},  # merged in from base, not the PR
        ]
        pr_net = {"src/a.rs", "src/b.rs"}
        kept, excluded = rereview.scope_delta_files(compare_files, pr_net)
        self.assertEqual([f["filename"] for f in kept], ["src/a.rs", "src/b.rs"])
        self.assertEqual(excluded, 1)

    def test_nothing_excluded_when_all_on_branch(self):
        import rereview

        files = [{"filename": "x"}, {"filename": "y"}]
        kept, excluded = rereview.scope_delta_files(files, {"x", "y"})
        self.assertEqual(excluded, 0)
        self.assertEqual(len(kept), 2)


class TestBuildDelta(unittest.TestCase):
    def _net(self):
        return [
            {
                "filename": "src/a.rs",
                "status": "modified",
                "additions": 10,
                "deletions": 2,
                "patch": "@@ -1,2 +1,10 @@\n+a",
            },
            {
                "filename": "src/big.rs",
                "status": "modified",
                "additions": 984,
                "deletions": 104,
                "patch": "@@ -1,1 +1,984 @@\n+big",
            },
        ]

    def test_no_anchor(self):
        import rereview

        d = rereview.build_delta(None, "H", None, [])
        self.assertEqual(d["files"], [])
        self.assertIn("nothing to anchor", d["note"])

    def test_head_unchanged(self):
        import rereview

        d = rereview.build_delta("H", "H", None, [])
        self.assertEqual(d["files"], [])
        self.assertIn("head unchanged", d["note"])

    def test_linear_ahead_scopes_to_author_files(self):
        import rereview

        cmp = {
            "status": "ahead",
            "commits": [{}, {}],
            "files": [
                {
                    "filename": "src/a.rs",
                    "status": "modified",
                    "additions": 3,
                    "deletions": 1,
                    "patch": "@@ delta @@\n+d",
                },
                {
                    "filename": "vendor/churn.rs",
                    "status": "modified",
                    "additions": 5,
                    "deletions": 0,
                    "patch": "@@ churn @@",
                },
            ],
        }
        d = rereview.build_delta("B", "H", cmp, self._net())
        self.assertFalse(d["anchor_orphaned"])
        self.assertEqual(d["compare_status"], "ahead")
        self.assertEqual([f["path"] for f in d["files"]], ["src/a.rs"])
        self.assertEqual(d["files_off_branch_excluded"], 1)
        # since-last-review patch is preserved, not the whole net patch
        self.assertIn("delta", d["files"][0]["patch"])

    def test_diverged_anchor_falls_back_to_full_net_diff(self):
        import rereview

        # Rebase orphaned the anchor: compare is a huge diverged range with the
        # author's files stripped to null patch / 0-0 counts.
        cmp = {
            "status": "diverged",
            "commits": [{}] * 67,
            "files": [
                {
                    "filename": "src/big.rs",
                    "status": "modified",
                    "additions": 0,
                    "deletions": 0,
                    "patch": None,
                },
                {
                    "filename": "base/unrelated.rs",
                    "status": "modified",
                    "additions": 0,
                    "deletions": 0,
                    "patch": None,
                },
            ],
        }
        d = rereview.build_delta("B", "H", cmp, self._net())
        self.assertTrue(d["anchor_orphaned"])
        self.assertEqual(d["compare_status"], "diverged")
        # full net diff, not the diverged compare's file set
        self.assertEqual(
            sorted(f["path"] for f in d["files"]), ["src/a.rs", "src/big.rs"]
        )
        big = next(f for f in d["files"] if f["path"] == "src/big.rs")
        self.assertEqual((big["additions"], big["deletions"]), (984, 104))
        self.assertTrue(big["patch"])  # real patch, not null
        self.assertIn("orphaned", d["note"])

    def test_null_patch_backfilled_from_net_even_when_ahead(self):
        import rereview

        # GitHub can omit patch on a large file even in a clean compare; backfill
        # it from the net diff rather than shipping a null patch.
        cmp = {
            "status": "ahead",
            "commits": [{}],
            "files": [
                {
                    "filename": "src/big.rs",
                    "status": "modified",
                    "additions": 984,
                    "deletions": 104,
                    "patch": None,
                }
            ],
        }
        d = rereview.build_delta("B", "H", cmp, self._net())
        self.assertEqual(len(d["files"]), 1)
        self.assertTrue(d["files"][0]["patch"])  # backfilled from net


if __name__ == "__main__":
    unittest.main()
