//go:build integration

package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint"
	"github.com/entireio/cli/cmd/entire/cli/checkpoint/id"
	"github.com/entireio/cli/cmd/entire/cli/gitrepo"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
)

const issueKeyPattern = `[A-Z][A-Z0-9]+-\d+`

// commitFlow drives one commit through the simulated git hook sequence with
// control over what the real hooks cannot be told apart by: the
// prepare-commit-msg source, the user's edit in the editor, --no-verify
// (commit-msg skipped), and amend.
type commitFlow struct {
	message  string              // initial message file content
	source   string              // prepare-commit-msg source ("" = editor flow)
	edit     func(string) string // the user's editor edit (nil = none)
	noVerify bool                // skip commit-msg, like git commit --no-verify
	amend    bool                // amend HEAD instead of committing
	files    []string            // files to stage first
}

// commitWithFlow runs prepare-commit-msg, the edit, commit-msg (unless
// noVerify), git's comment cleanup, the commit, and post-commit.
func (env *TestEnv) commitWithFlow(f commitFlow) {
	env.T.Helper()
	for _, file := range f.files {
		env.GitAdd(file)
	}
	msgFile := filepath.Join(env.RepoDir, ".git", "COMMIT_EDITMSG")
	if err := os.WriteFile(msgFile, []byte(f.message), 0o644); err != nil {
		env.T.Fatalf("write message: %v", err)
	}
	hookArgs := []string{msgFile}
	if f.source != "" {
		hookArgs = append(hookArgs, f.source)
	}
	// No TTY: a -m commit links without the interactive prompt.
	prep := env.prepareCommitMsgCmd(false, hookArgs...)
	if out, err := prep.CombinedOutput(); err != nil {
		env.T.Logf("prepare-commit-msg output: %s", out)
	}
	if f.edit != nil {
		content, err := os.ReadFile(msgFile)
		if err != nil {
			env.T.Fatalf("read message: %v", err)
		}
		if err := os.WriteFile(msgFile, []byte(f.edit(string(content))), 0o644); err != nil {
			env.T.Fatalf("write edited message: %v", err)
		}
	}
	if !f.noVerify {
		env.runCommitMsgHook(msgFile)
	}
	content, err := os.ReadFile(msgFile)
	if err != nil {
		env.T.Fatalf("read message: %v", err)
	}

	repo, err := gitrepo.OpenPath(env.RepoDir)
	if err != nil {
		env.T.Fatalf("open repo: %v", err)
	}
	defer repo.Close()
	worktree, err := repo.Worktree()
	if err != nil {
		env.T.Fatalf("worktree: %v", err)
	}
	if _, err := worktree.Commit(stripGitComments(string(content)), &git.CommitOptions{
		Author: &object.Signature{Name: testAuthorName, Email: testAuthorEmail, When: time.Now()},
		Amend:  f.amend,
	}); err != nil {
		env.T.Fatalf("commit: %v", err)
	}

	post := exec.CommandContext(env.T.Context(), getTestBinary(), "hooks", "git", "post-commit")
	post.Dir = env.RepoDir
	post.Env = env.gitHookEnv()
	if out, err := post.CombinedOutput(); err != nil {
		env.T.Logf("post-commit output: %s", out)
	}
}

// stripGitComments approximates git's default cleanup for an edited message:
// drop "#" lines and everything from the scissors line, trim blank edges.
func stripGitComments(message string) string {
	var kept []string
	for _, line := range strings.Split(message, "\n") {
		if strings.Contains(line, "------------------------ >8 ------------------------") {
			break
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Trim(strings.Join(kept, "\n"), "\n") + "\n"
}

// headMessage is HEAD's commit message, which real git seeds an amend with.
func (env *TestEnv) headMessage() string {
	env.T.Helper()
	return env.GetCommitMessage(env.GetHeadHash())
}

// sessionIssueKeys reads issue_key of every session of the checkpoint through
// the store abstraction, so it works for either backend.
func sessionIssueKeys(t *testing.T, env *TestEnv, cpID string) []string {
	t.Helper()
	repo, err := gitrepo.OpenPath(env.RepoDir)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer repo.Close()
	stores, err := checkpoint.Open(context.Background(), repo, checkpoint.OpenOptions{})
	if err != nil {
		t.Fatalf("open checkpoint stores: %v", err)
	}
	summary, err := stores.Persistent.Read(context.Background(), id.CheckpointID(cpID))
	if err != nil || summary == nil {
		t.Fatalf("read checkpoint %s: %v", cpID, err)
	}
	keys := make([]string, 0, len(summary.Sessions))
	for i := range summary.Sessions {
		meta, err := stores.Persistent.ReadSessionMetadata(context.Background(), id.CheckpointID(cpID), i)
		if err != nil {
			t.Fatalf("read session %d metadata: %v", i, err)
		}
		keys = append(keys, meta.IssueKey)
	}
	return keys
}

func assertIssueKeys(t *testing.T, env *TestEnv, cpID, want string) {
	t.Helper()
	keys := sessionIssueKeys(t, env, cpID)
	if len(keys) == 0 {
		t.Fatalf("checkpoint %s has no sessions", cpID)
	}
	for i, key := range keys {
		if key != want {
			t.Fatalf("session %d issue_key = %q, want %q (all: %q)", i, key, want, keys)
		}
	}
}

// issueKeyEnv sets up a repo on branch with issue_key_pattern and an agent
// turn that touched login.txt, ready to commit.
func issueKeyEnv(t *testing.T, backend, branch string) *TestEnv {
	t.Helper()
	env := NewRepoWithCommit(t)
	env.CheckpointStore = backend
	env.GitCheckoutNewBranch(branch)
	env.PatchSettings(map[string]any{"issue_key_pattern": issueKeyPattern})
	agentTurn(t, env, "login.txt", "content of login.txt")
	return env
}

func agentTurn(t *testing.T, env *TestEnv, file, content string) {
	t.Helper()
	session := env.NewSession()
	if err := env.SimulateUserPromptSubmit(session.ID); err != nil {
		t.Fatalf("SimulateUserPromptSubmit failed: %v", err)
	}
	env.WriteFile(file, content)
	session.CreateTranscript("Change "+file, []FileChange{{Path: file, Content: content}})
	if err := env.SimulateStop(session.ID, session.TranscriptPath); err != nil {
		t.Fatalf("SimulateStop failed: %v", err)
	}
}

func headCheckpointID(t *testing.T, env *TestEnv) string {
	t.Helper()
	cpID := env.GetCheckpointIDFromCommitMessage(env.GetHeadHash())
	if cpID == "" {
		t.Fatalf("HEAD should carry an Entire-Checkpoint trailer:\n%s", env.headMessage())
	}
	return cpID
}

func dropLines(prefix string) func(string) string {
	return func(msg string) string {
		var kept []string
		for _, line := range strings.Split(msg, "\n") {
			if !strings.HasPrefix(line, prefix) {
				kept = append(kept, line)
			}
		}
		return strings.Join(kept, "\n")
	}
}

// TestIssueKey_BranchKeyTrailerAndMetadata: the real hooks write the branch's
// key as an Entire-Issue trailer next to Entire-Checkpoint, and post-commit
// records it as issue_key, for both checkpoint backends.
func TestIssueKey_BranchKeyTrailerAndMetadata(t *testing.T) {
	t.Parallel()
	ForEachBackend(t, func(t *testing.T, backend string) {
		env := issueKeyEnv(t, backend, "feature/PROJ-77-login-timeout")
		env.GitCommitWithHooks("Fix login timeout", "login.txt")

		cpID := headCheckpointID(t, env)
		msg := env.headMessage()
		if !strings.Contains(msg, "Entire-Checkpoint: "+cpID+"\nEntire-Issue: PROJ-77") {
			t.Fatalf("expected Entire-Issue right after Entire-Checkpoint, got:\n%s", msg)
		}
		if strings.Count(msg, "Entire-Issue:") != 1 {
			t.Fatalf("expected exactly one Entire-Issue trailer, got:\n%s", msg)
		}
		assertIssueKeys(t, env, cpID, "PROJ-77")
	})
}

// TestIssueKey_AmendRemoveAndEdit: a key the user deletes or edits while
// amending stays deleted or edited, and the kept checkpoint's metadata follows
// the final trailer.
func TestIssueKey_AmendRemoveAndEdit(t *testing.T) {
	t.Parallel()
	ForEachBackend(t, func(t *testing.T, backend string) {
		env := issueKeyEnv(t, backend, "feature/PROJ-77-login")
		env.GitCommitWithHooks("PROJ-77 fix login", "login.txt")
		cpID := headCheckpointID(t, env)
		assertIssueKeys(t, env, cpID, "PROJ-77")

		// Amend, deleting the key in the editor (subject and branch still match).
		env.commitWithFlow(commitFlow{message: env.headMessage(), source: "commit", amend: true, edit: dropLines("Entire-Issue:")})
		if msg := env.headMessage(); strings.Contains(msg, "Entire-Issue:") {
			t.Fatalf("the deleted key came back:\n%s", msg)
		}
		if got := headCheckpointID(t, env); got != cpID {
			t.Fatalf("amend changed the checkpoint: %s -> %s", cpID, got)
		}
		assertIssueKeys(t, env, cpID, "")

		// Amend again without touching anything: still no key.
		env.commitWithFlow(commitFlow{message: env.headMessage(), source: "commit", amend: true})
		if msg := env.headMessage(); strings.Contains(msg, "Entire-Issue:") {
			t.Fatalf("a plain amend re-added the key:\n%s", msg)
		}

		// Amend, typing a different key.
		env.commitWithFlow(commitFlow{message: env.headMessage(), source: "commit", amend: true, edit: func(msg string) string {
			return strings.Replace(msg, "Entire-Checkpoint: "+cpID, "Entire-Checkpoint: "+cpID+"\nEntire-Issue: PROJ-78", 1)
		}})
		if msg := env.headMessage(); !strings.Contains(msg, "Entire-Issue: PROJ-78") || strings.Contains(msg, "PROJ-77\n\n") {
			t.Fatalf("expected the edited key, got:\n%s", msg)
		}
		assertIssueKeys(t, env, cpID, "PROJ-78")
	})
}

// TestIssueKey_EditorFlowSubjectFallback: with no key in the branch, the
// editor flow takes the key from the subject the user types (commit-msg), and
// --no-verify (no commit-msg) leaves the commit without one.
func TestIssueKey_EditorFlowSubjectFallback(t *testing.T) {
	t.Parallel()

	t.Run("commit-msg runs", func(t *testing.T) {
		t.Parallel()
		env := issueKeyEnv(t, StoreGitBranch, "feature-login")
		env.commitWithFlow(commitFlow{source: "", files: []string{"login.txt"}, edit: func(msg string) string {
			return "ENG-42 fix login" + msg
		}})
		cpID := headCheckpointID(t, env)
		if msg := env.headMessage(); !strings.Contains(msg, "Entire-Checkpoint: "+cpID+"\nEntire-Issue: ENG-42") {
			t.Fatalf("expected the subject's key, got:\n%s", msg)
		}
		assertIssueKeys(t, env, cpID, "ENG-42")
	})

	t.Run("no-verify", func(t *testing.T) {
		t.Parallel()
		env := issueKeyEnv(t, StoreGitBranch, "feature-login")
		env.commitWithFlow(commitFlow{source: "", noVerify: true, files: []string{"login.txt"}, edit: func(msg string) string {
			return "ENG-42 fix login" + msg
		}})
		cpID := headCheckpointID(t, env)
		if msg := env.headMessage(); strings.Contains(msg, "Entire-Issue") {
			t.Fatalf("--no-verify skips the subject fallback (and git strips its marker), got:\n%s", msg)
		}
		assertIssueKeys(t, env, cpID, "")
	})

	t.Run("no-verify keeps the branch key", func(t *testing.T) {
		t.Parallel()
		env := issueKeyEnv(t, StoreGitBranch, "feature/ENG-7-login")
		env.commitWithFlow(commitFlow{message: "fix login\n", source: "message", noVerify: true, files: []string{"login.txt"}})
		cpID := headCheckpointID(t, env)
		if msg := env.headMessage(); !strings.Contains(msg, "Entire-Issue: ENG-7") {
			t.Fatalf("prepare-commit-msg writes the branch key even with --no-verify, got:\n%s", msg)
		}
		assertIssueKeys(t, env, cpID, "ENG-7")
	})
}

// TestIssueKey_SquashMessageQuotingOtherKeys: a `merge --squash` style body
// that quotes another commit's (indented) Entire-Issue line neither blocks the
// commit's own key nor becomes its metadata. (A quoted Entire-Checkpoint line
// is left out: ParseCheckpoint still scans the whole message, so it would be
// taken as an existing link — a pre-existing behavior, not this feature's.)
func TestIssueKey_SquashMessageQuotingOtherKeys(t *testing.T) {
	t.Parallel()
	env := issueKeyEnv(t, StoreGitBranch, "feature/PROJ-9-squash")
	message := "Squash PROJ-9 work\n\nSquashed commit of the following:\n\ncommit 1111111\n\n    old work\n\n" +
		"    Entire-Issue: OLD-1\n"
	env.commitWithFlow(commitFlow{message: message, source: "message", files: []string{"login.txt"}})

	cpID := headCheckpointID(t, env)
	msg := env.headMessage()
	if !strings.Contains(msg, "Entire-Checkpoint: "+cpID+"\nEntire-Issue: PROJ-9") {
		t.Fatalf("expected the commit's own key in its trailer block, got:\n%s", msg)
	}
	assertIssueKeys(t, env, cpID, "PROJ-9")
}

// TestIssueKey_InvalidPatternKeepsHooksWorking: an invalid pattern in the
// shared settings turns off issue linking only; commits are still linked to
// sessions, and doctor reports the pattern.
func TestIssueKey_InvalidPatternKeepsHooksWorking(t *testing.T) {
	t.Parallel()
	env := NewRepoWithCommit(t)
	env.GitCheckoutNewBranch("feature/PROJ-1-x")
	env.PatchSettings(map[string]any{"issue_key_pattern": `[A-Z\d+`})
	agentTurn(t, env, "a.txt", "a")
	env.GitCommitWithHooks("PROJ-1 change", "a.txt")

	headCheckpointID(t, env)
	if msg := env.headMessage(); strings.Contains(msg, "Entire-Issue") {
		t.Fatalf("an invalid pattern must not link issues, got:\n%s", msg)
	}
	out, err := env.RunCLIWithError("doctor")
	if err != nil {
		t.Logf("doctor exited with %v (other checks may fail in a test repo)", err)
	}
	if !strings.Contains(out, "Issue key pattern: INVALID") {
		t.Fatalf("doctor should report the invalid pattern, got:\n%s", out)
	}
}
