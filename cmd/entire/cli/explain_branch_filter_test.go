package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint"
	"github.com/entireio/cli/cmd/entire/cli/checkpoint/id"
	"github.com/entireio/cli/cmd/entire/cli/strategy"
	"github.com/entireio/cli/cmd/entire/cli/testutil"
	"github.com/entireio/cli/cmd/entire/cli/trailers"
)

// branchFilterRepo is a repo with one checkpoint on the default branch and
// two on an unmerged feature branch (one by Alice, one by Bob).
type branchFilterRepo struct {
	dir           string
	defaultBranch string
	mainCP        id.CheckpointID
	aliceCP       id.CheckpointID
	bobCP         id.CheckpointID
}

const branchFilterFeature = "feature/branch-filter"

// setupBranchFilterRepo builds the branchFilterRepo layout in a temp repo
// (CWD is moved into it) and leaves HEAD on the default branch.
func setupBranchFilterRepo(t *testing.T) branchFilterRepo {
	t.Helper()
	repo, tmpDir := setupCheckpointListRepo(t)

	defaultBranch := strategy.GetCurrentBranchName(repo)
	require.NotEmpty(t, defaultBranch)

	w, err := repo.Worktree()
	require.NoError(t, err)
	store := checkpoint.NewGitStore(repo, checkpoint.DefaultV1Refs())
	base := time.Now().Add(-time.Hour)

	commitCheckpoint := func(raw, sessionID, msg, name, email string, offset time.Duration) id.CheckpointID {
		t.Helper()
		cpID, err := id.NewCheckpointID(raw)
		require.NoError(t, err)
		require.NoError(t, store.Write(context.Background(), checkpoint.Session{
			CheckpointID: cpID,
			SessionID:    sessionID,
			Strategy:     "manual-commit",
			FilesTouched: []string{"test.txt"},
			Prompts:      []string{"prompt for " + sessionID},
		}))
		testutil.WriteFile(t, tmpDir, "test.txt", msg)
		testutil.GitAdd(t, tmpDir, "test.txt")
		sig := &object.Signature{Name: name, Email: email, When: base.Add(offset)}
		_, err = w.Commit(trailers.FormatCheckpoint(msg, cpID), &git.CommitOptions{Author: sig, Committer: sig})
		require.NoError(t, err)
		return cpID
	}

	mainCP := commitCheckpoint("d0d0d0d0d0d0", "2026-10-09-main-session", "Main change", "Carol Main", "carol@example.com", time.Minute)

	testutil.GitCheckoutNewBranch(t, tmpDir, branchFilterFeature)
	aliceCP := commitCheckpoint("a1a1a1a1a1a1", "2026-10-09-alice-session", "Alice change", "Alice Smith", "alice@example.com", 2*time.Minute)
	bobCP := commitCheckpoint("b2b2b2b2b2b2", "2026-10-09-bob-session", "Bob change", "Bob Jones", "bob@example.com", 3*time.Minute)

	testutil.RunGit(t, tmpDir, "checkout", defaultBranch)

	return branchFilterRepo{dir: tmpDir, defaultBranch: defaultBranch, mainCP: mainCP, aliceCP: aliceCP, bobCP: bobCP}
}

// listJSONCheckpointIDs runs `checkpoint list --json <args>` and returns the
// checkpoint IDs in output order.
func listJSONCheckpointIDs(t *testing.T, args ...string) []string {
	t.Helper()
	var out []branchCheckpointJSON
	require.NoError(t, json.Unmarshal([]byte(runListCmd(t, append([]string{"--json"}, args...)...)), &out))
	ids := make([]string, 0, len(out))
	for _, e := range out {
		ids = append(ids, e.CheckpointID)
	}
	return ids
}

func TestCheckpointListBranch_FeatureBranchFromDefault(t *testing.T) {
	r := setupBranchFilterRepo(t)

	// HEAD is on the default branch: only its own checkpoint is listed.
	require.Equal(t, []string{r.mainCP.String()}, listJSONCheckpointIDs(t))

	// --branch lists the feature branch as if it were checked out: its own
	// checkpoints, newest first, without the one it shares with the default.
	require.Equal(t, []string{r.bobCP.String(), r.aliceCP.String()},
		listJSONCheckpointIDs(t, "--branch", branchFilterFeature))

	// The human view's header names the requested branch.
	out := runListCmd(t, "--no-pager", "--branch", branchFilterFeature)
	require.Contains(t, out, branchFilterFeature)
	require.Contains(t, out, "Alice change")
	require.Contains(t, out, "Bob change")
	require.NotContains(t, out, "Main change")
}

func TestCheckpointListBranch_DefaultBranchFromFeature(t *testing.T) {
	r := setupBranchFilterRepo(t)
	testutil.RunGit(t, r.dir, "checkout", branchFilterFeature)

	// Sanity: from the feature branch, the default view is the feature's own.
	require.Equal(t, []string{r.bobCP.String(), r.aliceCP.String()}, listJSONCheckpointIDs(t))

	// Naming the default branch takes the default-branch path from its tip:
	// the unmerged feature checkpoints are not in its history.
	require.Equal(t, []string{r.mainCP.String()}, listJSONCheckpointIDs(t, "--branch", r.defaultBranch))

	out := runListCmd(t, "--no-pager", "--branch", r.defaultBranch)
	require.Contains(t, out, "Main change")
	require.NotContains(t, out, "Bob change")
}

func TestCheckpointListBranch_DefaultBranchUsesFullWalk(t *testing.T) {
	r := setupBranchFilterRepo(t)

	// Merge the feature into the default branch with a merge commit: the
	// feature's checkpoints are second-parent history, which only the
	// default-branch (full DAG) walk reaches.
	testutil.RunGit(t, r.dir, "merge", "--no-ff", "-m", "Merge feature", branchFilterFeature)
	testutil.RunGit(t, r.dir, "checkout", "-b", "other")

	ids := listJSONCheckpointIDs(t, "--branch", r.defaultBranch)
	require.ElementsMatch(t, []string{r.mainCP.String(), r.aliceCP.String(), r.bobCP.String()}, ids)
}

func TestCheckpointListBranch_RemoteFallback(t *testing.T) {
	r := setupBranchFilterRepo(t)

	// A branch that exists only as origin/<name> resolves through the
	// remote-tracking ref.
	tip := testutil.RunGit(t, r.dir, "rev-parse", branchFilterFeature)
	testutil.GitUpdateRef(t, r.dir, "refs/remotes/origin/remote-only", strings.TrimSpace(tip))

	require.Equal(t, []string{r.bobCP.String(), r.aliceCP.String()},
		listJSONCheckpointIDs(t, "--branch", "remote-only"))
}

func TestCheckpointListBranch_UnknownBranchErrors(t *testing.T) {
	setupBranchFilterRepo(t)

	for _, extra := range [][]string{nil, {"--json"}} {
		cmd := newCheckpointListCmd()
		cmd.SetArgs(append([]string{"--no-pager", "--branch", "does-not-exist"}, extra...))
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		err := cmd.Execute()
		require.Error(t, err)
		require.Contains(t, err.Error(), `branch "does-not-exist" not found`)
		require.Contains(t, err.Error(), "deleted")
	}
}

func TestCheckpointListBranch_WithAuthorAndSession(t *testing.T) {
	r := setupBranchFilterRepo(t)

	require.Equal(t, []string{r.bobCP.String()},
		listJSONCheckpointIDs(t, "--branch", branchFilterFeature, "--author", "BOB@"))
	require.Equal(t, []string{r.aliceCP.String()},
		listJSONCheckpointIDs(t, "--branch", branchFilterFeature, "--session", "2026-10-09-alice"))
	require.Empty(t, listJSONCheckpointIDs(t, "--branch", branchFilterFeature, "--author", "carol"))

	out := runListCmd(t, "--no-pager", "--branch", branchFilterFeature, "--author", "alice")
	require.Contains(t, out, "Alice change")
	require.NotContains(t, out, "Bob change")
}

func TestCheckpointListBranch_WithPendingErrors(t *testing.T) {
	setupCheckpointListRepo(t)

	cmd := newCheckpointListCmd()
	cmd.SetArgs([]string{"--pending", "--branch", "main"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "--branch cannot be combined with --pending")
}

func TestResolveBranchListTarget_RejectsInvalidName(t *testing.T) {
	repo, _ := setupCheckpointListRepo(t)

	_, err := resolveBranchListTarget(repo, "../escape")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid branch name")
}
