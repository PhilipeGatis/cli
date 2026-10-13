package strategy

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/testutil"

	"github.com/go-git/go-git/v6/plumbing/object"
)

func TestMatchIssueKey(t *testing.T) {
	t.Parallel()

	jira := regexp.MustCompile(`[A-Z][A-Z0-9]+-\d+`)
	tests := []struct {
		name string
		re   *regexp.Regexp
		in   string
		want string
	}{
		{name: "whole match", re: jira, in: "feat/PROJ-123-login-timeout", want: "PROJ-123"},
		{name: "first match", re: jira, in: "PROJ-1-and-PROJ-2", want: "PROJ-1"},
		{name: "no match", re: jira, in: "main", want: ""},
		{name: "nil pattern", re: nil, in: "PROJ-1", want: ""},
		{name: "empty input", re: jira, in: "", want: ""},
		{name: "capture group 1 is the key", re: regexp.MustCompile(`(?:^|/)gh-(\d+)`), in: "feat/gh-42-login", want: "42"},
		{name: "only group 1 of several", re: regexp.MustCompile(`([A-Z]+)-(\d+)`), in: "x/ENG-7", want: "ENG"},
		{name: "non-participating group 1", re: regexp.MustCompile(`(ENG-\d+)?main`), in: "main", want: ""},
		{name: "non-capturing group uses whole match", re: regexp.MustCompile(`(?:PROJ|ENG)-\d+`), in: "a/ENG-9", want: "ENG-9"},
		{name: "match with whitespace rejected", re: regexp.MustCompile(`PROJ \d+`), in: "PROJ 5 thing", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := matchIssueKey(tt.re, tt.in); got != tt.want {
				t.Errorf("matchIssueKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCommitSubject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, msg, prefix, want string
	}{
		{name: "first line", msg: "ENG-42: fix login\n\nbody PROJ-7\n", prefix: "#", want: "ENG-42: fix login"},
		{name: "skips comments and blank lines", msg: "\n# comment ABC-1\nfix ENG-42\n", prefix: "#", want: "fix ENG-42"},
		{name: "custom comment char", msg: "; comment ABC-1\n#42 fix ENG-42\n", prefix: ";", want: "#42 fix ENG-42"},
		{name: "-m message keeps # lines as content", msg: "#42 ENG-1 fix\n", prefix: "", want: "#42 ENG-1 fix"},
		{name: "stops at scissors", msg: "\n# ------------------------ >8 ------------------------\nENG-1\n", prefix: "#", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := commitSubject(tt.msg, tt.prefix); got != tt.want {
				t.Errorf("commitSubject() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAutoCommentPrefix(t *testing.T) {
	t.Parallel()

	if got := autoCommentPrefix("fix\n\n; Please enter the commit message\n;\n"); got != ";" {
		t.Errorf("autoCommentPrefix(;) = %q", got)
	}
	if got := autoCommentPrefix("fix\n\nEntire-Checkpoint: abc123def456\n"); got != "#" {
		t.Errorf("autoCommentPrefix(no comments) = %q, want #", got)
	}
}

func TestPostCommitHandlerIssueKey(t *testing.T) {
	t.Parallel()

	h := &postCommitActionHandler{commit: &object.Commit{Message: "feat\n\nEntire-Checkpoint: abc123def456\nEntire-Issue: PROJ-9\n"}}
	if got := h.issueKey(); got != "PROJ-9" {
		t.Errorf("issueKey() = %q, want PROJ-9", got)
	}
	if got := (&postCommitActionHandler{commit: &object.Commit{Message: "feat\n"}}).issueKey(); got != "" {
		t.Errorf("issueKey() without trailer = %q, want empty", got)
	}
	if got := (&postCommitActionHandler{}).issueKey(); got != "" {
		t.Errorf("issueKey() without commit = %q, want empty", got)
	}
	squash := "Squashed commit of the following:\n\n    Entire-Checkpoint: aaaaaaaaaaaa\n    Entire-Issue: OLD-1\n\nEntire-Checkpoint: bbbbbbbbbbbb\n"
	if got := (&postCommitActionHandler{commit: &object.Commit{Message: squash}}).issueKey(); got != "" {
		t.Errorf("issueKey() of a squash message quoting another key = %q, want empty", got)
	}
}

// setupIssueKeyRepo creates a repo with one commit on branch, optional
// settings, and chdirs into it. Returns the path of a commit message file.
func setupIssueKeyRepo(t *testing.T, settingsJSON, branch string) string {
	t.Helper()
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	if settingsJSON != "" {
		testutil.WriteFile(t, dir, ".entire/settings.json", settingsJSON)
	}
	testutil.WriteFile(t, dir, "a.txt", "a")
	testutil.GitAdd(t, dir, "a.txt")
	testutil.GitCommit(t, dir, "initial")
	if branch != "" {
		testutil.GitCheckoutNewBranch(t, dir, branch)
	}
	t.Chdir(dir)
	return filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
}

func writeMsg(t *testing.T, msgFile, message string) {
	t.Helper()
	if err := os.WriteFile(msgFile, []byte(message), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readMsg(t *testing.T, msgFile string) string {
	t.Helper()
	out, err := os.ReadFile(msgFile)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// runOffer simulates prepare-commit-msg: git handed it before, and the
// checkpoint part of the hook turned it into after.
func runOffer(t *testing.T, msgFile, source, before, after string) string {
	t.Helper()
	writeMsg(t, msgFile, after)
	offerIssueTrailer(context.Background(), msgFile, source, before)
	return readMsg(t, msgFile)
}

func runCommitMsg(t *testing.T, msgFile, message string) string {
	t.Helper()
	writeMsg(t, msgFile, message)
	if err := NewManualCommitStrategy().CommitMsg(context.Background(), msgFile); err != nil {
		t.Fatalf("CommitMsg() error = %v", err)
	}
	return readMsg(t, msgFile)
}

const issuePatternSettings = `{"enabled": true, "issue_key_pattern": "[A-Z][A-Z0-9]+-\\d+"}`

const (
	cpTrailer = "Entire-Checkpoint: abc123def456\n"
	// editorComments mimics addCheckpointTrailerWithComment plus git's help text.
	editorComments = "# Remove the Entire-Checkpoint trailer above if you don't want to link this commit.\n\n# Please enter the commit message for your changes.\n"
)

func TestOfferIssueTrailer_FromBranch(t *testing.T) {
	msgFile := setupIssueKeyRepo(t, issuePatternSettings, "feat/PROJ-123-login")

	got := runOffer(t, msgFile, "message", "fix login\n", "fix login\n\n"+cpTrailer)
	if want := "fix login\n\n" + cpTrailer + "Entire-Issue: PROJ-123\n"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}

	// Editor flow: the key is visible (and editable) next to the trailer.
	got = runOffer(t, msgFile, "", editorComments, "\n\n"+cpTrailer+editorComments)
	if want := "\n\n" + cpTrailer + "Entire-Issue: PROJ-123\n" + editorComments; got != want {
		t.Fatalf("editor message = %q, want %q", got, want)
	}
}

func TestOfferIssueTrailer_MessageSubjectFallback(t *testing.T) {
	msgFile := setupIssueKeyRepo(t, issuePatternSettings, "")

	got := runOffer(t, msgFile, "message", "ENG-42 fix login\n", "ENG-42 fix login\n\n"+cpTrailer)
	if want := "ENG-42 fix login\n\n" + cpTrailer + "Entire-Issue: ENG-42\n"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

func TestOfferIssueTrailer_CaptureGroup(t *testing.T) {
	msgFile := setupIssueKeyRepo(t, `{"enabled": true, "issue_key_pattern": "(?:^|/)gh-(\\d+)"}`, "feat/gh-77-login")

	got := runOffer(t, msgFile, "message", "fix\n", "fix\n\n"+cpTrailer)
	if want := "fix\n\n" + cpTrailer + "Entire-Issue: 77\n"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

func TestOfferIssueTrailer_LeavesExistingLinksAlone(t *testing.T) {
	msgFile := setupIssueKeyRepo(t, issuePatternSettings, "feat/PROJ-123-login")

	t.Run("amend keeps a message whose key the user deleted", func(t *testing.T) {
		msg := "fix login\n\n" + cpTrailer
		if got := runOffer(t, msgFile, "commit", msg, msg); got != msg {
			t.Fatalf("message changed: %q", got)
		}
	})
	t.Run("amend keeps an edited key", func(t *testing.T) {
		msg := "fix login\n\n" + cpTrailer + "Entire-Issue: OTHER-9\n"
		if got := runOffer(t, msgFile, "commit", msg, msg); got != msg {
			t.Fatalf("message changed: %q", got)
		}
	})
	t.Run("user-supplied key", func(t *testing.T) {
		before := "fix login\n\nEntire-Issue: MINE-1\n"
		after := before + cpTrailer
		if got := runOffer(t, msgFile, "message", before, after); got != after {
			t.Fatalf("message changed: %q", got)
		}
	})
	t.Run("no checkpoint trailer added", func(t *testing.T) {
		msg := "fix login\n"
		if got := runOffer(t, msgFile, "message", msg, msg); got != msg {
			t.Fatalf("message changed: %q", got)
		}
	})
}

func TestOfferIssueTrailer_PatternUnsetOrInvalid(t *testing.T) {
	for name, settingsJSON := range map[string]string{
		"unset":   `{"enabled": true}`,
		"invalid": `{"enabled": true, "issue_key_pattern": "[A-Z"}`,
	} {
		t.Run(name, func(t *testing.T) {
			msgFile := setupIssueKeyRepo(t, settingsJSON, "feat/PROJ-123-login")
			after := "fix login\n\n" + cpTrailer
			if got := runOffer(t, msgFile, "message", "fix login\n", after); got != after {
				t.Fatalf("message changed: %q", got)
			}
		})
	}
}

// Editor flow with no key in the branch: prepare leaves a marker, commit-msg
// replaces it with the subject's key.
func TestIssueSubjectMarker_EditorFlow(t *testing.T) {
	msgFile := setupIssueKeyRepo(t, issuePatternSettings, "feature-login")

	prepared := runOffer(t, msgFile, "", editorComments, "\n\n"+cpTrailer+editorComments)
	wantPrepared := "\n\n" + cpTrailer + "# " + issueSubjectMarker + "\n" + editorComments
	if prepared != wantPrepared {
		t.Fatalf("prepared = %q, want %q", prepared, wantPrepared)
	}

	t.Run("subject has a key", func(t *testing.T) {
		got := runCommitMsg(t, msgFile, "ENG-42 fix login"+prepared)
		if want := "ENG-42 fix login\n\n" + cpTrailer + "Entire-Issue: ENG-42\n" + editorComments; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
	})
	t.Run("subject has no key", func(t *testing.T) {
		got := runCommitMsg(t, msgFile, "fix login"+prepared)
		if want := "fix login\n\n" + cpTrailer + editorComments; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
	})
	t.Run("user deleted the marker", func(t *testing.T) {
		msg := "ENG-42 fix login\n\n" + cpTrailer + editorComments
		if got := runCommitMsg(t, msgFile, msg); got != msg {
			t.Fatalf("message changed: %q", got)
		}
	})
	t.Run("user typed a key", func(t *testing.T) {
		got := runCommitMsg(t, msgFile, "ENG-42 fix login\n\n"+cpTrailer+"Entire-Issue: OWN-1\n# "+issueSubjectMarker+"\n"+editorComments)
		if want := "ENG-42 fix login\n\n" + cpTrailer + "Entire-Issue: OWN-1\n" + editorComments; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
	})
	t.Run("user deleted the checkpoint trailer", func(t *testing.T) {
		got := runCommitMsg(t, msgFile, "ENG-42 fix login\n\n# "+issueSubjectMarker+"\n"+editorComments)
		if want := "ENG-42 fix login\n\n" + editorComments; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
	})
}

// commit-msg never adds a key without prepare's marker: an amend whose user
// deleted the key (and kept a subject that matches) stays without one.
func TestCommitMsg_NoMarkerNoKey(t *testing.T) {
	msgFile := setupIssueKeyRepo(t, issuePatternSettings, "feat/PROJ-123-login")

	msg := "ENG-42 fix login\n\n" + cpTrailer
	if got := runCommitMsg(t, msgFile, msg); got != msg {
		t.Fatalf("message changed: %q", got)
	}
}

func TestIssueSubjectMarker_CustomCommentChar(t *testing.T) {
	msgFile := setupIssueKeyRepo(t, issuePatternSettings, "feature-login")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	testutil.RunGit(t, wd, "config", "core.commentChar", ";")

	comments := "; Please enter the commit message for your changes.\n"
	prepared := runOffer(t, msgFile, "", comments, "\n\n"+cpTrailer+comments)
	if want := "\n\n" + cpTrailer + "; " + issueSubjectMarker + "\n" + comments; prepared != want {
		t.Fatalf("prepared = %q, want %q", prepared, want)
	}
	// A subject starting with "#" is content, not a comment, under ";".
	got := runCommitMsg(t, msgFile, "#12 ENG-42 fix"+prepared)
	if want := "#12 ENG-42 fix\n\n" + cpTrailer + "Entire-Issue: ENG-42\n" + comments; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

func TestOfferIssueTrailer_CRLF(t *testing.T) {
	msgFile := setupIssueKeyRepo(t, issuePatternSettings, "feat/PROJ-5")

	got := runOffer(t, msgFile, "message", "fix\r\n", "fix\r\n\r\nEntire-Checkpoint: abc123def456\r\n")
	if want := "fix\r\n\r\nEntire-Checkpoint: abc123def456\r\nEntire-Issue: PROJ-5\r\n"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

func TestCommitMsg_TrailerOnlyMessageStripsIssueTrailerToo(t *testing.T) {
	msgFile := setupIssueKeyRepo(t, issuePatternSettings, "feat/PROJ-123-login")

	got := runCommitMsg(t, msgFile, "\nEntire-Checkpoint: abc123def456\nEntire-Issue: PROJ-123\n# comment\n")
	if got != "\n# comment\n" {
		t.Fatalf("message = %q, want only the comment left so git aborts", got)
	}
}
