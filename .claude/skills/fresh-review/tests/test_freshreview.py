"""Tests for the pure helpers in the fresh-review packet builder. Run:
    python3 .claude/skills/fresh-review/tests/test_freshreview.py
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "scripts"))

import freshreview  # noqa: E402


class TestPartitionFiles(unittest.TestCase):
    def test_omitted_files_are_surfaced_not_dropped(self):
        files = [
            {"filename": "src/a.rs", "patch": "@@ -1 +1 @@\n-x\n+y"},
            {"filename": "assets/logo.png"},          # binary: no patch key
            {"filename": "huge.bin", "patch": None},   # too large: patch is null
        ]
        reviewable, omitted = freshreview.partition_files(files)
        self.assertEqual([f["filename"] for f in reviewable], ["src/a.rs"])
        # The unreadable files are reported, so the resident can say what it
        # could NOT review rather than silently skipping them.
        self.assertEqual(sorted(omitted), ["assets/logo.png", "huge.bin"])

    def test_all_reviewable(self):
        files = [{"filename": "x", "patch": "p"}, {"filename": "y", "patch": "q"}]
        reviewable, omitted = freshreview.partition_files(files)
        self.assertEqual(len(reviewable), 2)
        self.assertEqual(omitted, [])


class TestTruncatePatch(unittest.TestCase):
    def test_short_patch_unchanged(self):
        patch = "\n".join(f"+line{i}" for i in range(10))
        out, truncated = freshreview._truncate_patch(patch)
        self.assertEqual(out, patch)
        self.assertFalse(truncated)

    def test_long_patch_truncated_with_flag(self):
        patch = "\n".join(f"+line{i}" for i in range(freshreview.MAX_PATCH_LINES + 50))
        out, truncated = freshreview._truncate_patch(patch)
        self.assertEqual(len(out.splitlines()), freshreview.MAX_PATCH_LINES)
        self.assertTrue(truncated)

    def test_empty_patch(self):
        self.assertEqual(freshreview._truncate_patch(None), (None, False))


if __name__ == "__main__":
    unittest.main()
