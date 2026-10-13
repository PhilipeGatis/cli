package strategy

// Issue-key linking (issue_key_pattern). The rule for when an Entire-Issue
// trailer is written, so that a key the user removes or edits stays removed or
// edited:
//
//   - prepare-commit-msg offers the key, and only when that same invocation
//     added the commit's Entire-Checkpoint trailer (a message that arrives with
//     one — an amend, a reworded commit — is never touched). The key comes from
//     the branch name; for a `-m`/`-F` message (source "message") whose branch
//     has none, from the subject, which is already final. The user sees and can
//     edit or delete the line in the editor.
//   - In the editor flow the subject is not written yet, so when the branch has
//     no key prepare-commit-msg leaves a marker comment instead, and commit-msg
//     (the first hook that sees the final subject) replaces it with the
//     subject's key. No marker, no fallback: commit-msg never adds a key on its
//     own, so it cannot re-add one an amend removed.
//   - post-commit records exactly the final trailer as issue_key (or none), and
//     rewrites the metadata of a checkpoint an amend kept when the trailer
//     changed.
//
// `git commit --no-verify` skips only commit-msg: branch keys are still written,
// the subject fallback of the editor flow is not (git strips the marker).

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint"
	"github.com/entireio/cli/cmd/entire/cli/checkpoint/id"
	"github.com/entireio/cli/cmd/entire/cli/logging"
	"github.com/entireio/cli/cmd/entire/cli/settings"
	"github.com/entireio/cli/cmd/entire/cli/trailers"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

// issueSubjectMarker is the text of the comment line prepare-commit-msg leaves
// (after the comment prefix and a space) when the key must come from a subject
// the user has not written yet.
const issueSubjectMarker = "Entire-Issue: taken from the subject at commit if it matches issue_key_pattern (delete this line to skip)"

// autoCommentChars are the characters git picks from for core.commentChar=auto.
const autoCommentChars = "#;@!$%^&|:"

// matchIssueKey returns the issue key re finds in s: capture group 1 when the
// pattern has a capture group, else the whole match. Returns "" when re is nil,
// nothing matches, group 1 did not participate, or the key contains
// whitespace (it could not round-trip through an Entire-Issue trailer).
func matchIssueKey(re *regexp.Regexp, s string) string {
	if re == nil || s == "" {
		return ""
	}
	m := re.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	key := m[0]
	if re.NumSubexp() >= 1 {
		key = m[1]
	}
	if strings.ContainsFunc(key, unicode.IsSpace) {
		return ""
	}
	return key
}

// commitSubject returns the first non-blank line of message that does not
// start with commentPrefix (pass "" when the message has no comment lines,
// e.g. a `-m` message, where git keeps "#" lines as content), stopping at a
// scissors line.
func commitSubject(message, commentPrefix string) string {
	for _, line := range strings.Split(message, "\n") {
		if strings.Contains(line, "------------------------ >8 ------------------------") {
			return ""
		}
		if commentPrefix != "" && strings.HasPrefix(line, commentPrefix) {
			continue
		}
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// gitCommentPrefix returns the prefix git uses for comment lines in the
// commit message being edited: core.commentString, else core.commentChar,
// else "#". For "auto" git picks a character not starting any line of the
// initial message, which the hook cannot see; the character starting the last
// comment-looking line of message (git's help text comes last) is used.
func gitCommentPrefix(ctx context.Context, message string) string {
	value := gitConfigValue(ctx, "core.commentString")
	if value == "" {
		value = gitConfigValue(ctx, "core.commentChar")
	}
	switch value {
	case "":
		return "#"
	case "auto":
		return autoCommentPrefix(message)
	default:
		return value
	}
}

// autoCommentPrefix guesses the core.commentChar=auto character from the
// message's last non-blank line; "#" when it does not start with a candidate.
func autoCommentPrefix(message string) string {
	lines := strings.Split(strings.TrimRight(message, "\r\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.ContainsRune(autoCommentChars, rune(line[0])) {
			return line[:1]
		}
		break
	}
	return "#"
}

// headBranchName returns the branch HEAD points at, including an unborn
// branch (a repository's first commit), or "" when HEAD is detached or
// unreadable. GetCurrentBranchName cannot be used here: it resolves HEAD,
// which fails before the first commit exists.
func headBranchName(ctx context.Context) string {
	repo, err := OpenRepository(ctx)
	if err != nil {
		return ""
	}
	defer repo.Close()
	ref, err := repo.Storer.Reference(plumbing.HEAD)
	if err != nil || ref.Type() != plumbing.SymbolicReference || !ref.Target().IsBranch() {
		return ""
	}
	return ref.Target().Short()
}

// issueKeyRegexp loads the configured issue_key_pattern for a commit hook.
// Returns nil when issue linking is off: the setting is empty, settings cannot
// be loaded, or the pattern is invalid. An invalid pattern turns off issue
// linking only (settings load accepts it so the other hooks keep working);
// it is logged and reported on stderr in one line so the commit author sees it.
func issueKeyRegexp(ctx context.Context) *regexp.Regexp {
	logCtx := logging.WithComponent(ctx, "checkpoint")
	s, err := settings.Load(ctx)
	if err != nil {
		logging.Debug(logCtx, "issue key: settings unavailable, skipping issue trailer",
			slog.String("error", err.Error()))
		return nil
	}
	re, err := s.IssueKeyRegexp()
	if err != nil {
		logging.Warn(logCtx, "issue key: invalid issue_key_pattern, issue linking is off",
			slog.String("error", err.Error()))
		fmt.Fprintf(os.Stderr, "[entire] Warning: %v; issue linking is off (fix it in .entire/settings.json)\n", err)
		return nil
	}
	return re
}

// offerIssueTrailer runs at the end of prepare-commit-msg. before is the
// message as git handed it to the hook. See the rule at the top of this file.
func offerIssueTrailer(ctx context.Context, commitMsgFile, source, before string) {
	if trailers.HasCheckpointTrailerLine(before) {
		return
	}
	content, err := os.ReadFile(commitMsgFile) //nolint:gosec // Path comes from git hook
	if err != nil {
		return
	}
	message := string(content)
	if !trailers.HasCheckpointTrailerLine(message) {
		return
	}
	re := issueKeyRegexp(ctx)
	if re == nil {
		return
	}
	commentPrefix := gitCommentPrefix(ctx, message)
	if _, found := trailers.ParseDraftIssue(message, commentPrefix); found {
		return
	}

	updated := message
	if key := matchIssueKey(re, headBranchName(ctx)); key != "" {
		updated = trailers.AppendIssueTrailer(message, key)
	} else {
		switch source {
		case commitSourceMessage:
			// The subject of a -m/-F message is final; its "#" lines are content.
			updated = trailers.AppendIssueTrailer(message, matchIssueKey(re, commitSubject(message, "")))
		case "", "template", "squash":
			updated = addIssueSubjectMarker(message, commentPrefix)
		}
	}
	if updated == message {
		return
	}
	if err := os.WriteFile(commitMsgFile, []byte(updated), 0o600); err != nil { //nolint:gosec // path from git hook arg
		return
	}
	logging.Debug(logging.WithComponent(ctx, "checkpoint"), "prepare-commit-msg: issue trailer offered",
		slog.String("source", source))
}

// addIssueSubjectMarker inserts the marker comment directly after the last
// Entire-Checkpoint trailer line, where commit-msg puts the key.
func addIssueSubjectMarker(message, commentPrefix string) string {
	lines := strings.Split(message, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if !trailers.HasCheckpointTrailerLine(lines[i]) {
			continue
		}
		marker := commentPrefix + " " + issueSubjectMarker
		if strings.HasSuffix(lines[i], "\r") {
			marker += "\r"
		}
		return strings.Join(slices.Insert(lines, i+1, marker), "\n")
	}
	return message
}

// takeIssueSubjectMarker removes the marker comment from message, reporting
// whether it was there.
func takeIssueSubjectMarker(message string) (string, bool) {
	lines := strings.Split(message, "\n")
	for i, line := range lines {
		if strings.HasSuffix(strings.TrimRight(line, "\r"), " "+issueSubjectMarker) {
			return strings.Join(slices.Delete(lines, i, i+1), "\n"), true
		}
	}
	return message, false
}

// applyIssueSubjectFallback runs in commit-msg on a message that still has
// user content, after takeIssueSubjectMarker removed prepare-commit-msg's
// marker (marked). When the marker was there and the message has a checkpoint
// trailer but no Entire-Issue trailer, it adds the subject's key. Without a
// marker the message is returned unchanged.
func applyIssueSubjectFallback(ctx context.Context, message string, marked bool) string {
	if !marked || !trailers.HasCheckpointTrailerLine(message) {
		return message
	}
	commentPrefix := gitCommentPrefix(ctx, message)
	if _, found := trailers.ParseDraftIssue(message, commentPrefix); found {
		return message
	}
	re := issueKeyRegexp(ctx)
	if re == nil {
		return message
	}
	return trailers.AppendIssueTrailer(message, matchIssueKey(re, commitSubject(message, commentPrefix)))
}

// syncIssueKeyMetadata makes the issue_key of every session in checkpointID
// equal key (the commit's final Entire-Issue trailer, "" for none). It runs
// in post-commit for a checkpoint that existed before the commit (an amend
// that kept its trailer), whose metadata condensation did not, or only
// partly, rewrite. Skipped when issue linking is off and the commit carries no
// key, so repositories without the setting pay nothing. Best-effort.
func (s *ManualCommitStrategy) syncIssueKeyMetadata(ctx context.Context, repo *git.Repository, checkpointID id.CheckpointID, key string) {
	logCtx := logging.WithComponent(ctx, "checkpoint")
	if key == "" {
		stngs, err := settings.Load(ctx)
		if err != nil || stngs.IssueKeyPattern == "" {
			return
		}
	}
	store, err := s.getPersistentStore(ctx, repo)
	if err != nil {
		return
	}
	summary, err := store.Read(ctx, checkpointID)
	if err != nil || summary == nil {
		return
	}
	stale := false
	for i := range summary.Sessions {
		meta, metaErr := store.ReadSessionMetadata(ctx, checkpointID, i)
		if metaErr != nil {
			return
		}
		if meta.IssueKey != key {
			stale = true
			break
		}
	}
	if !stale {
		return
	}
	if err := store.Write(ctx, checkpoint.SessionIssueKey{CheckpointID: checkpointID, IssueKey: key}); err != nil {
		logging.Warn(logCtx, "post-commit: failed to update checkpoint issue key",
			slog.String("checkpoint_id", checkpointID.String()),
			slog.String("error", err.Error()))
		return
	}
	logging.Info(logCtx, "post-commit: checkpoint issue key follows the amended trailer",
		slog.String("checkpoint_id", checkpointID.String()),
		slog.Bool("cleared", key == ""))
}
