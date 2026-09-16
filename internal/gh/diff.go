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

// SplitUnifiedDiff splits a raw `git diff` (the PR .diff media type) into one
// hunks-only patch per file, keyed by path, in the same shape as the REST
// files API's "patch" field: starting at the first "@@" with no file headers
// and no trailing newline. Files with no hunks (binary, mode-only changes) are
// absent from the map. It is the fallback source of a patch GitHub omits from
// the files API because the file's diff is too large.
func SplitUnifiedDiff(diff string) map[string]string {
	patches := map[string]string{}
	var path string
	var hunks []string
	flush := func() {
		if path != "" && len(hunks) > 0 {
			patches[path] = strings.Join(hunks, "\n")
		}
		path, hunks = "", nil
	}
	inHunks := false
	var oldPath string
	for _, ln := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(ln, "diff --git "):
			flush()
			inHunks = false
			oldPath = ""
		case !inHunks && strings.HasPrefix(ln, "--- "):
			oldPath = diffHeaderPath(strings.TrimPrefix(ln, "--- "))
		case !inHunks && strings.HasPrefix(ln, "+++ "):
			// A deleted file's new side is /dev/null; key it by its old path.
			if p := diffHeaderPath(strings.TrimPrefix(ln, "+++ ")); p != "" {
				path = p
			} else {
				path = oldPath
			}
		case strings.HasPrefix(ln, "@@"):
			inHunks = true
			hunks = append(hunks, ln)
		case inHunks:
			hunks = append(hunks, ln)
		}
	}
	flush()
	for p, patch := range patches {
		patches[p] = strings.TrimRight(patch, "\n")
	}
	return patches
}

// diffHeaderPath reads the repo-relative path out of a "---"/"+++" header
// operand ("a/foo.go", `"b/with space.go"`). It returns "" for /dev/null.
func diffHeaderPath(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) {
		if unquoted, err := strconv.Unquote(s); err == nil {
			s = unquoted
		}
	}
	if s == "/dev/null" {
		return ""
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[i+1:] // strip the a/ or b/ prefix
	}
	return s
}
