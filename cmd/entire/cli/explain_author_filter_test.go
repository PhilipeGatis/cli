package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
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

func TestCheckpointMatchesAuthorFilter(t *testing.T) {
	t.Parallel()

	p := strategy.PendingCheckpoint{AuthorName: "Alice Smith", AuthorEmail: "alice@example.com"}
	tests := []struct {
		filter string
		want   bool
	}{
		{"alice", true},
		{"SMITH", true},
		{"@example.com", true},
		{"bob", false},
	}
	for _, tt := range tests {
		if got := checkpointMatchesAuthorFilter(p, tt.filter); got != tt.want {
			t.Errorf("checkpointMatchesAuthorFilter(%q) = %v, want %v", tt.filter, got, tt.want)
		}
	}

	if checkpointMatchesAuthorFilter(strategy.PendingCheckpoint{Imported: true}, "alice") {
		t.Error("expected a point without an author not to match a non-empty filter")
	}
}

// seedAuthoredCheckpoints creates one committed checkpoint by Alice and a newer
// one by Bob on the current branch, and returns their checkpoint IDs.
func seedAuthoredCheckpoints(t *testing.T, tmpDir string, repo *git.Repository) (id.CheckpointID, id.CheckpointID) {
	t.Helper()

	w, err := repo.Worktree()
	require.NoError(t, err)

	testFile := filepath.Join(tmpDir, "test.txt")
	commit := func(msg, name, email string, when time.Time, content string) {
		t.Helper()
		require.NoError(t, os.WriteFile(testFile, []byte(content), 0o644))
		_, err := w.Add("test.txt")
		require.NoError(t, err)
		_, err = w.Commit(msg, &git.CommitOptions{
			Author:    &object.Signature{Name: name, Email: email, When: when},
			Committer: &object.Signature{Name: name, Email: email, When: when},
		})
		require.NoError(t, err)
	}

	base := time.Now().Add(-time.Hour)
	commit("initial commit", "Test", "test@example.com", base, "initial")

	store := checkpoint.NewGitStore(repo, checkpoint.DefaultV1Refs())
	write := func(raw, sessionID string) id.CheckpointID {
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
		return cpID
	}

	aliceCP := write("a1a1a1a1a1a1", "2026-10-09-alice-session")
	commit(trailers.FormatCheckpoint("Alice change", aliceCP), "Alice Smith", "alice@example.com", base.Add(time.Minute), "alice")

	bobCP := write("b2b2b2b2b2b2", "2026-10-09-bob-session")
	commit(trailers.FormatCheckpoint("Bob change", bobCP), "Bob Jones", "bob@example.com", base.Add(2*time.Minute), "bob")

	return aliceCP, bobCP
}

func TestGetBranchCheckpointsByAuthor_FiltersBeforeLimit(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	aliceCP, bobCP := seedAuthoredCheckpoints(t, tmpDir, repo)

	all, _, err := getBranchCheckpoints(context.Background(), repo, 10)
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, bobCP, all[0].CheckpointID, "newest first")
	require.Equal(t, "Bob Jones", all[0].AuthorName)
	require.Equal(t, "bob@example.com", all[0].AuthorEmail)

	// With limit 1, Bob's newer checkpoint would evict Alice's if the filter
	// ran after truncation.
	points, _, err := getBranchCheckpointsByAuthor(context.Background(), repo, 1, "ALICE")
	require.NoError(t, err)
	require.Len(t, points, 1)
	require.Equal(t, aliceCP, points[0].CheckpointID)
	require.Equal(t, "Alice Smith", points[0].AuthorName)

	none, _, err := getBranchCheckpointsByAuthor(context.Background(), repo, 10, "carol")
	require.NoError(t, err)
	require.Empty(t, none)
}

func TestRunExplainListJSON_AuthorFilterAndFields(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	aliceCP, _ := seedAuthoredCheckpoints(t, tmpDir, repo)

	var stdout bytes.Buffer
	require.NoError(t, runExplainListJSON(context.Background(), &stdout, io.Discard, "", "alice@", 0))

	var out []branchCheckpointJSON
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &out))
	require.Len(t, out, 1)
	require.Equal(t, aliceCP.String(), out[0].CheckpointID)
	require.Equal(t, "Alice Smith", out[0].AuthorName)
	require.Equal(t, "alice@example.com", out[0].AuthorEmail)
}
