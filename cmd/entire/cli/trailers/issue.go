package trailers

import (
	"regexp"
	"strings"
	"unicode"
)

// blockTrailerLineRegex matches the start of a trailer line inside a trailer
// block: a token at column 0 followed by a colon. Unlike trailerLineRe it
// accepts "Key:value" without a space, as git does.
var blockTrailerLineRegex = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*:`)

// cherryPickedPrefix starts the note `git cherry-pick -x` appends, which git
// accepts inside a trailer block.
const cherryPickedPrefix = "(cherry picked from commit "

// trailerBlockLines returns the indexes (into strings.Split(message, "\n")) of
// the lines of message's final trailer block, following git
// interpret-trailers: everything from a scissors line down is ignored, lines
// starting with commentPrefix are ignored (pass "" for a committed message,
// which has none), and the block is the last paragraph, provided it is not the
// subject paragraph, has at least one trailer ("Key: value" starting at
// column 0), and every other line is a continuation line (leading
// whitespace, never a trailer itself) or a cherry-pick note. Returns nil when
// the message has no trailer block.
//
// Deliberately stricter than git in one way: git also accepts a mixed last
// paragraph when at least a quarter of it is trailers and one is git-generated.
// Entire-Issue is only ever looked up here, never in such a mixed block, so
// a quoted or indented "Entire-Issue:" line in a body (e.g. the commit list
// `git merge --squash` writes) is never mistaken for the commit's own.
func trailerBlockLines(message, commentPrefix string) []int {
	lines := strings.Split(message, "\n")
	var kept []int // non-comment line indexes above the scissors line
	for i, line := range lines {
		if strings.Contains(line, scissorsMarker) {
			break
		}
		if commentPrefix != "" && strings.HasPrefix(line, commentPrefix) {
			continue
		}
		kept = append(kept, i)
	}
	text := func(k int) string { return strings.TrimRight(lines[kept[k]], "\r") }
	blank := func(k int) bool { return strings.TrimSpace(text(k)) == "" }

	end := len(kept)
	for end > 0 && blank(end-1) {
		end--
	}
	start := end
	for start > 0 && !blank(start-1) {
		start--
	}
	if start == end {
		return nil
	}
	// The subject paragraph is never a trailer block.
	subject := true
	for k := range start {
		if !blank(k) {
			subject = false
			break
		}
	}
	if subject {
		return nil
	}

	block := make([]int, 0, end-start)
	hasTrailer := false
	for k := start; k < end; k++ {
		line := text(k)
		switch {
		case blockTrailerLineRegex.MatchString(line):
			hasTrailer = true
		case strings.HasPrefix(line, cherryPickedPrefix):
		case line[0] == ' ' || line[0] == '\t':
			// A continuation line: of the trailer above it, or (at the top of
			// the paragraph) of nothing, which git also tolerates. Never a
			// trailer itself.
		default:
			return nil
		}
		block = append(block, kept[k])
	}
	if !hasTrailer {
		return nil
	}
	return block
}

// ParseIssue extracts the key of the first Entire-Issue trailer in a committed
// message's final trailer block (see trailerBlockLines). Returns the key and
// true if found, empty string and false otherwise. A value that is empty or
// contains whitespace is not a key.
func ParseIssue(commitMessage string) (string, bool) {
	return ParseDraftIssue(commitMessage, "")
}

// ParseDraftIssue is ParseIssue for a message that still carries git's
// comment lines (inside the prepare-commit-msg and commit-msg hooks), which
// start with commentPrefix and are ignored.
func ParseDraftIssue(message, commentPrefix string) (string, bool) {
	lines := strings.Split(message, "\n")
	for _, i := range trailerBlockLines(message, commentPrefix) {
		key, value, ok := strings.Cut(strings.TrimRight(lines[i], "\r"), ":")
		if !ok || !strings.EqualFold(key, IssueTrailerKey) {
			continue
		}
		value = strings.TrimSpace(value)
		if value != "" && !strings.ContainsFunc(value, unicode.IsSpace) {
			return value, true
		}
	}
	return "", false
}

// AppendIssueTrailer adds `Entire-Issue: <key>` to message; an empty or
// whitespace-bearing key is a no-op. Callers check for an existing
// Entire-Issue trailer first (ParseDraftIssue): this function does not, so a
// caller that decided to replace one can.
//
// The line is inserted directly after the last Entire-Checkpoint trailer line
// (starting at column 0) above any scissors line, so the two stay in one
// trailer paragraph even when git's comment block (stripped later) follows the
// trailers. Without a checkpoint trailer it is appended in trailer-block-aware
// format. The inserted line uses the message's line ending (CRLF or LF).
func AppendIssueTrailer(message, key string) string {
	if key == "" || strings.ContainsFunc(key, unicode.IsSpace) {
		return message
	}
	trailerLine := IssueTrailerKey + ": " + key

	lines := strings.Split(message, "\n")
	end := len(lines)
	for i, line := range lines {
		if strings.Contains(line, scissorsMarker) {
			end = i
			break
		}
	}
	insertAfter := func(i int) string {
		inserted := trailerLine
		if strings.HasSuffix(lines[i], "\r") {
			inserted += "\r"
		}
		out := make([]string, 0, len(lines)+1)
		out = append(out, lines[:i+1]...)
		out = append(out, inserted)
		out = append(out, lines[i+1:]...)
		return strings.Join(out, "\n")
	}
	for i := end - 1; i >= 0; i-- {
		if checkpointTrailerLineRegex.MatchString(strings.TrimRight(lines[i], "\r")) {
			return insertAfter(i)
		}
	}
	if block := trailerBlockLines(message, "#"); len(block) > 0 {
		return insertAfter(block[len(block)-1])
	}
	eol := "\n"
	if strings.Contains(message, "\r\n") {
		eol = "\r\n"
	}
	return strings.TrimRight(message, "\r\n") + eol + eol + trailerLine + eol
}

// HasCheckpointTrailerLine reports whether message has a whole
// Entire-Checkpoint trailer line starting at column 0 above any scissors line.
// Indented copies (the commit list `git merge --squash` writes into its
// message) do not count.
func HasCheckpointTrailerLine(message string) bool {
	for _, line := range strings.Split(message, "\n") {
		if strings.Contains(line, scissorsMarker) {
			return false
		}
		if checkpointTrailerLineRegex.MatchString(strings.TrimRight(line, "\r")) {
			return true
		}
	}
	return false
}
