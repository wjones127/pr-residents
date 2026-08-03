package gh

import "testing"

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
