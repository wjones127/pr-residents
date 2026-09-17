package gh

import (
	"strings"
	"testing"
)

func TestCommentableLines(t *testing.T) {
	// A hunk starting at new line 10: context, removal, addition, context.
	//   @@ -10,3 +10,3 @@
	//    a      -> old 10 / new 10 (context)
	//   -b      -> old 11        (removed)
	//   +c      ->        new 11 (added)
	//    d      -> old 12 / new 12 (context)
	patch := "@@ -10,3 +10,3 @@\n a\n-b\n+c\n d\n"
	right, left := CommentableLines(patch)

	for _, ln := range []int{10, 11, 12} {
		if !right[ln] {
			t.Errorf("RIGHT %d should be commentable", ln)
		}
	}
	for _, ln := range []int{10, 11, 12} {
		if !left[ln] {
			t.Errorf("LEFT %d should be commentable", ln)
		}
	}
	// The line just past the hunk (the "comment on the whole file" overrun) is not.
	if right[13] {
		t.Error("RIGHT 13 is past the hunk and must not be commentable")
	}
	if right[9] {
		t.Error("RIGHT 9 is before the hunk and must not be commentable")
	}
}

func TestCommentableLinesMultipleHunks(t *testing.T) {
	patch := "@@ -1,2 +1,2 @@\n x\n+y\n@@ -50,1 +51,2 @@\n z\n+w\n"
	right, _ := CommentableLines(patch)
	want := map[int]bool{1: true, 2: true, 51: true, 52: true}
	for ln := range want {
		if !right[ln] {
			t.Errorf("RIGHT %d should be commentable", ln)
		}
	}
	// Nothing between the two hunks.
	if right[10] || right[49] || right[53] {
		t.Error("lines between/after hunks must not be commentable")
	}
}

// A single-line hunk header omits the ",count" (e.g. "@@ -1 +1 @@").
func TestCommentableLinesSingleLineHeader(t *testing.T) {
	right, left := CommentableLines("@@ -1 +1 @@\n-x\n+y\n")
	if !right[1] || !left[1] {
		t.Errorf("single-line header: right=%v left=%v", right, left)
	}
}

func TestCommentableLinesEmpty(t *testing.T) {
	right, left := CommentableLines("")
	if len(right) != 0 || len(left) != 0 {
		t.Errorf("empty patch should yield no lines: right=%v left=%v", right, left)
	}
}

func TestSplitUnifiedDiff(t *testing.T) {
	raw := `diff --git a/a.go b/a.go
index 111..222 100644
--- a/a.go
+++ b/a.go
@@ -1,2 +1,2 @@
 keep
-old
+new
@@ -20,1 +20,2 @@
 ctx
+added
diff --git a/img.png b/img.png
index 333..444 100644
Binary files a/img.png and b/img.png differ
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
--- a/gone.txt
+++ /dev/null
@@ -1,1 +0,0 @@
-bye
diff --git "a/with space.go" "b/with space.go"
--- "a/with space.go"
+++ "b/with space.go"
@@ -1 +1 @@
-x
+y
`
	got := SplitUnifiedDiff(raw)

	wantA := "@@ -1,2 +1,2 @@\n keep\n-old\n+new\n@@ -20,1 +20,2 @@\n ctx\n+added"
	if got["a.go"] != wantA {
		t.Errorf("a.go patch:\n%q\nwant:\n%q", got["a.go"], wantA)
	}
	if _, ok := got["img.png"]; ok {
		t.Errorf("binary file should have no patch: %q", got["img.png"])
	}
	if want := "@@ -1,1 +0,0 @@\n-bye"; got["gone.txt"] != want {
		t.Errorf("deleted file keyed/parsed wrong: %q", got["gone.txt"])
	}
	if want := "@@ -1 +1 @@\n-x\n+y"; got["with space.go"] != want {
		t.Errorf("quoted path: %q", got["with space.go"])
	}
}

func TestSplitUnifiedDiffKeepsHeaderLikeContent(t *testing.T) {
	// A hunk body line that itself looks like a diff header must stay in the
	// patch: content lines carry a leading +/-/space.
	raw := `diff --git a/doc.md b/doc.md
--- a/doc.md
+++ b/doc.md
@@ -1,2 +1,3 @@
 intro
+--- a/example.txt
+++ b/example.txt
`
	got := SplitUnifiedDiff(raw)
	if !strings.Contains(got["doc.md"], "+--- a/example.txt") {
		t.Errorf("header-like content dropped: %q", got["doc.md"])
	}
	if len(got) != 1 {
		t.Errorf("header-like content started a new file: %v", got)
	}
}
