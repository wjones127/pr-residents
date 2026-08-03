package gh

import (
	"strconv"
	"strings"
)

// CommentableLines parses a unified-diff patch into the line numbers a review
// comment may anchor to, per side. A comment is valid on any line shown inside a
// hunk: added/context lines on the RIGHT (new) side, removed/context lines on the
// LEFT (old) side. Lines outside every hunk — notably a number just past the last
// hunk, which an agent sometimes picks to "comment on the whole file" — are not
// returned, so a caller can reject them before GitHub 422s the whole review.
func CommentableLines(patch string) (right, left map[int]bool) {
	right, left = map[int]bool{}, map[int]bool{}
	var oldLn, newLn int
	inHunk := false
	for _, ln := range strings.Split(patch, "\n") {
		if strings.HasPrefix(ln, "@@") {
			o, n, ok := parseHunkHeader(ln)
			if !ok {
				inHunk = false
				continue
			}
			oldLn, newLn, inHunk = o, n, true
			continue
		}
		if !inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(ln, "+"):
			right[newLn] = true
			newLn++
		case strings.HasPrefix(ln, "-"):
			left[oldLn] = true
			oldLn++
		case strings.HasPrefix(ln, " "):
			right[newLn] = true
			left[oldLn] = true
			oldLn++
			newLn++
		default:
			// "\ No newline at end of file", the trailing split artifact, or an
			// elision marker — no line-number movement.
		}
	}
	return right, left
}

// parseHunkHeader reads the old/new start lines from a "@@ -a,b +c,d @@" header
// (each ",count" is optional). ok is false for a malformed header.
func parseHunkHeader(h string) (oldStart, newStart int, ok bool) {
	fields := strings.Fields(h)
	if len(fields) < 3 || fields[0] != "@@" {
		return 0, 0, false
	}
	o, ok1 := parseSideStart(fields[1], '-')
	n, ok2 := parseSideStart(fields[2], '+')
	return o, n, ok1 && ok2
}

// parseSideStart reads the start line from a hunk-header side token like
// "-12,7" or "+14" (sign is '-' for the old side, '+' for the new).
func parseSideStart(f string, sign byte) (int, bool) {
	if len(f) == 0 || f[0] != sign {
		return 0, false
	}
	f = f[1:]
	if i := strings.IndexByte(f, ','); i >= 0 {
		f = f[:i]
	}
	n, err := strconv.Atoi(f)
	if err != nil {
		return 0, false
	}
	return n, true
}
