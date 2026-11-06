package cli

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"

	"github.com/entireio/cli/cmd/entire/cli/gitrepo"
	"github.com/entireio/cli/cmd/entire/cli/strategy"
	"github.com/entireio/cli/cmd/entire/cli/testutil"
)

// initWalkRepo returns a fresh repo (no CWD change) and a helper that adds an
// empty commit on HEAD's branch and returns its hash.
func initWalkRepo(t *testing.T) (*git.Repository, func(msg string) plumbing.Hash) {
	t.Helper()
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	repo, err := gitrepo.OpenPath(dir)
	require.NoError(t, err)
	w, err := repo.Worktree()
	require.NoError(t, err)

	base := time.Now().Add(-time.Hour)
	n := 0
	commit := func(msg string) plumbing.Hash {
		t.Helper()
		n++
		sig := &object.Signature{Name: "Test", Email: "test@test.com", When: base.Add(time.Duration(n) * time.Second)}
		h, err := w.Commit(msg, &git.CommitOptions{Author: sig, Committer: sig, AllowEmptyCommits: true})
		require.NoError(t, err)
		return h
	}
	return repo, commit
}

func TestFirstParentUnion_KeepsEachTipsOwnWindow(t *testing.T) {
	t.Parallel()
	repo, commit := initWalkRepo(t)

	c := make([]plumbing.Hash, 6)
	for i := range c {
		c[i] = commit(fmt.Sprintf("c%d", i))
	}
	want := map[plumbing.Hash]bool{c[5]: true, c[4]: true, c[3]: true, c[2]: true, c[1]: true}

	// c5 is a local default branch two commits ahead of c3 (origin). With a
	// window of 3, c5 covers c5..c3 and c3 covers c3..c1. The origin walk
	// meets c3, which the local walk reached two steps deep, at its own tip,
	// so it must keep going to reach c2 and c1.
	got := firstParentUnion(context.Background(), repo, []plumbing.Hash{c[5], c[3]}, 3)
	require.Equal(t, want, got)

	// Walked the other way round, the later walk stops early at c3 and the
	// result is the same.
	got = firstParentUnion(context.Background(), repo, []plumbing.Hash{c[3], c[5]}, 3)
	require.Equal(t, want, got)

	// The same tip twice adds nothing.
	got = firstParentUnion(context.Background(), repo, []plumbing.Hash{c[5], c[5]}, 3)
	require.Equal(t, map[plumbing.Hash]bool{c[5]: true, c[4]: true, c[3]: true}, got)
}

func TestWalkBranchCommits_ReportsScanLimit(t *testing.T) {
	t.Parallel()

	t.Run("default branch", func(t *testing.T) {
		t.Parallel()
		repo, commit := initWalkRepo(t)
		var tip plumbing.Hash
		for i := range 4 {
			tip = commit(fmt.Sprintf("c%d", i))
		}

		for _, tc := range []struct {
			scanLimit   int
			wantVisited int
			wantLimited bool
		}{
			{scanLimit: 2, wantVisited: 2, wantLimited: true},
			{scanLimit: 4, wantVisited: 4, wantLimited: false},
		} {
			visited := 0
			limited, err := walkBranchListCommits(context.Background(), repo, tip, true, tc.scanLimit, func(*object.Commit) { visited++ })
			require.NoError(t, err)
			require.Equal(t, tc.wantVisited, visited, "scanLimit %d", tc.scanLimit)
			require.Equal(t, tc.wantLimited, limited, "scanLimit %d", tc.scanLimit)
		}
	})

	t.Run("feature branch", func(t *testing.T) {
		t.Parallel()
		repo, commit := initWalkRepo(t)
		commit("c0")
		defaultBranch := strategy.GetCurrentBranchName(repo)
		require.NotEmpty(t, defaultBranch)
		mainTip := commit("c1")
		commit("f1")
		commit("f2")
		featureTip := commit("f3")
		// Move the default branch back to c1, leaving f1..f3 as feature work.
		require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(defaultBranch), mainTip)))

		for _, tc := range []struct {
			scanLimit   int
			wantVisited int
			wantLimited bool
		}{
			// f1 is still left to scan.
			{scanLimit: 2, wantVisited: 2, wantLimited: true},
			// The walk reaches the default branch at c1 before the budget runs out.
			{scanLimit: 3, wantVisited: 3, wantLimited: false},
		} {
			visited := 0
			limited, err := walkBranchListCommits(context.Background(), repo, featureTip, false, tc.scanLimit, func(*object.Commit) { visited++ })
			require.NoError(t, err)
			require.Equal(t, tc.wantVisited, visited, "scanLimit %d", tc.scanLimit)
			require.Equal(t, tc.wantLimited, limited, "scanLimit %d", tc.scanLimit)
		}
	})
}

// TestCheckpointListAuthor_NotesScanLimit: an --author search that runs out of
// commit budget says so on stderr instead of reporting a complete search.
// Uses t.Chdir — do NOT add t.Parallel.
func TestCheckpointListAuthor_NotesScanLimit(t *testing.T) {
	repo, _ := setupCheckpointListRepo(t)
	w, err := repo.Worktree()
	require.NoError(t, err)
	sig := &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()}
	for i := range commitScanLimit {
		_, err := w.Commit(fmt.Sprintf("empty %d", i), &git.CommitOptions{Author: sig, Committer: sig, AllowEmptyCommits: true})
		require.NoError(t, err)
	}

	run := func(args ...string) string {
		t.Helper()
		cmd := newCheckpointListCmd()
		var stdout, stderr bytes.Buffer
		cmd.SetArgs(args)
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		require.NoError(t, cmd.Execute(), "checkpoint list %v; stderr: %s", args, stderr.String())
		return stderr.String()
	}

	require.Contains(t, run("--no-pager", "--author", "nobody"), authorScanLimitNote)
	require.Contains(t, run("--json", "--author", "nobody"), authorScanLimitNote)

	// Without --author nothing changes: there are fewer checkpoints than the
	// list limit, so no note.
	require.NotContains(t, run("--no-pager"), "note:")
	require.NotContains(t, run("--json"), "note:")
}
