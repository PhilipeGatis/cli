package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint"
	checkpointid "github.com/entireio/cli/cmd/entire/cli/checkpoint/id"
	"github.com/entireio/cli/cmd/entire/cli/testutil"
	"github.com/entireio/cli/cmd/entire/cli/trailers"
	"github.com/entireio/cli/redact"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
)

// issueSeed is one committed checkpoint: metaKey goes to the session
// metadata's issue_key, trailerKey to the commit's Entire-Issue trailer.
type issueSeed struct {
	cpID       string
	subject    string
	metaKey    string
	trailerKey string
}

// issueBaseTime spaces seeded commits an hour apart so newest-first order is
// deterministic.
var issueBaseTime = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// seedIssueCheckpoint writes s to the checkpoint store and commits a change on
// the current branch whose message links it, at issueBaseTime + n hours.
func seedIssueCheckpoint(t *testing.T, repo *git.Repository, dir string, s issueSeed, n int) {
	t.Helper()
	when := issueBaseTime.Add(time.Duration(n) * time.Hour)
	cpID := checkpointid.MustCheckpointID(s.cpID)
	require.NoError(t, checkpoint.NewGitStore(repo, checkpoint.DefaultV1Refs()).Write(context.Background(), checkpoint.Session(checkpoint.WriteOptions{
		CheckpointID: cpID,
		SessionID:    "2026-10-09-issue-" + s.cpID,
		Strategy:     "manual-commit",
		CreatedAt:    when,
		Transcript:   redact.AlreadyRedacted([]byte(`{"test": true}` + "\n")),
		Prompts:      []string{"prompt " + s.subject},
		FilesTouched: []string{"test.txt"},
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
		IssueKey:     s.metaKey,
	})))
	msg := trailers.FormatCheckpoint(s.subject, cpID)
	if s.trailerKey != "" {
		msg = trailers.AppendIssueTrailer(msg, s.trailerKey)
	}
	testutil.WriteFile(t, dir, "test.txt", "change "+s.cpID)
	testutil.GitAdd(t, dir, "test.txt")
	w, err := repo.Worktree()
	require.NoError(t, err)
	sig := &object.Signature{Name: "Test", Email: "test@test.com", When: when}
	_, err = w.Commit(msg, &git.CommitOptions{Author: sig, Committer: sig})
	require.NoError(t, err)
}

func listJSON(t *testing.T, args ...string) []branchCheckpointJSON {
	t.Helper()
	out := runListCmd(t, append([]string{"--json"}, args...)...)
	var entries []branchCheckpointJSON
	require.NoError(t, json.Unmarshal([]byte(out), &entries), "invalid JSON: %s", out)
	return entries
}

func checkpointIDs(entries []branchCheckpointJSON) []string {
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.CheckpointID)
	}
	return ids
}

// Uses t.Chdir — do NOT add t.Parallel().
func TestCheckpointList_IssueFilter(t *testing.T) {
	repo, dir := setupCheckpointListRepo(t)
	for i, s := range []issueSeed{
		{cpID: "a1a1a1a1a1a1", subject: "Trailer-linked change", trailerKey: "PROJ-1"},
		{cpID: "b2b2b2b2b2b2", subject: "Metadata-linked change", metaKey: "ENG-42"},
		{cpID: "c3c3c3c3c3c3", subject: "Unlinked change"},
	} {
		seedIssueCheckpoint(t, repo, dir, s, i)
	}

	// The branch list carries issue_key too: trailer, else metadata.
	keys := map[string]string{}
	for _, e := range listJSON(t) {
		keys[e.CheckpointID] = e.IssueKey
	}
	require.Equal(t, map[string]string{
		"a1a1a1a1a1a1": "PROJ-1",
		"b2b2b2b2b2b2": "ENG-42",
		"c3c3c3c3c3c3": "",
	}, keys)
	require.Equal(t, 2, strings.Count(runListCmd(t, "--json"), `"issue_key"`), "issue_key must be omitted when empty")

	byTrailer := listJSON(t, "--issue", "proj-1")
	require.Equal(t, []string{"a1a1a1a1a1a1"}, checkpointIDs(byTrailer), "case-insensitive match on the commit trailer")
	require.Equal(t, "PROJ-1", byTrailer[0].IssueKey)

	require.Equal(t, []string{"b2b2b2b2b2b2"}, checkpointIDs(listJSON(t, "--issue", "ENG-42")), "match on metadata issue_key")
	require.Empty(t, listJSON(t, "--issue", "PROJ-12"), "exact match, not prefix/substring")

	human := runListCmd(t, "--no-pager", "--issue", "PROJ-1")
	require.Contains(t, human, "Trailer-linked change")
	require.Contains(t, human, "PROJ-1")
	require.NotContains(t, human, "Metadata-linked change")
	require.NotContains(t, human, "Unlinked change")
}

// --issue searches every branch, and the whole store once a branch is gone.
// Uses t.Chdir — do NOT add t.Parallel().
func TestCheckpointList_IssueAcrossBranches(t *testing.T) {
	repo, dir := setupCheckpointListRepo(t)
	mainBranch := strings.TrimSpace(testutil.RunGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD"))

	// A live feature branch: only its commit's trailer links the checkpoint.
	testutil.GitCheckoutNewBranch(t, dir, "feat/PROJ-7-live")
	seedIssueCheckpoint(t, repo, dir, issueSeed{cpID: "d4d4d4d4d4d4", subject: "Live branch work", trailerKey: "PROJ-7"}, 1)

	// A deleted feature branch: its commit is unreachable, metadata remains.
	testutil.RunGit(t, dir, "checkout", "-q", mainBranch)
	testutil.GitCheckoutNewBranch(t, dir, "feat/PROJ-7-gone")
	seedIssueCheckpoint(t, repo, dir, issueSeed{cpID: "e5e5e5e5e5e5", subject: "Deleted branch work", metaKey: "PROJ-7", trailerKey: "PROJ-7"}, 2)
	testutil.RunGit(t, dir, "checkout", "-q", mainBranch)
	testutil.RunGit(t, dir, "branch", "-q", "-D", "feat/PROJ-7-gone")

	// The current branch (main) has neither commit.
	require.Empty(t, listJSON(t), "the branch list stays scoped to the current branch")

	entries := listJSON(t, "--issue", "PROJ-7")
	require.Equal(t, []string{"e5e5e5e5e5e5", "d4d4d4d4d4d4"}, checkpointIDs(entries), "newest first, from both branches")
	require.Equal(t, "Live branch work", entries[1].Message)
	require.Equal(t, "prompt Deleted branch work", entries[0].Message, "a commit-less match shows its prompt")

	human := runListCmd(t, "--no-pager", "--issue", "PROJ-7")
	require.Contains(t, human, "Live branch work")
	require.Contains(t, human, "all")
}

// The trailer is the user-visible link: for a checkpoint on a reachable
// commit it wins over stale metadata.
// Uses t.Chdir — do NOT add t.Parallel().
func TestCheckpointList_IssueTrailerWinsOverMetadata(t *testing.T) {
	repo, dir := setupCheckpointListRepo(t)
	seedIssueCheckpoint(t, repo, dir, issueSeed{cpID: "f6f6f6f6f6f6", subject: "Edited key", metaKey: "OLD-1", trailerKey: "NEW-2"}, 0)

	require.Empty(t, listJSON(t, "--issue", "OLD-1"))
	require.Equal(t, []string{"f6f6f6f6f6f6"}, checkpointIDs(listJSON(t, "--issue", "NEW-2")))
}

// The filter runs before the limit, the newest matches are kept, and
// truncation is reported. Uses t.Chdir — do NOT add t.Parallel().
func TestGetIssueCheckpoints_FilterBeforeLimit(t *testing.T) {
	repo, dir := setupCheckpointListRepo(t)
	for i, s := range []issueSeed{
		{cpID: "a1a1a1a1a1a1", subject: "Older match", trailerKey: "PROJ-1"},
		{cpID: "b2b2b2b2b2b2", subject: "Newer match", trailerKey: "PROJ-1"},
		{cpID: "c3c3c3c3c3c3", subject: "Newest unrelated 1"},
		{cpID: "d4d4d4d4d4d4", subject: "Newest unrelated 2"},
	} {
		seedIssueCheckpoint(t, repo, dir, s, i)
	}
	ctx := context.Background()
	open, err := openRepository(ctx)
	require.NoError(t, err)
	defer open.Close()

	points, truncated, err := getIssueCheckpoints(ctx, open, 1, "PROJ-1")
	require.NoError(t, err)
	require.True(t, truncated)
	require.Len(t, points, 1)
	require.Equal(t, "b2b2b2b2b2b2", points[0].CheckpointID.String(), "the newest match survives the limit")

	points, truncated, err = getIssueCheckpoints(ctx, open, 10, "PROJ-1")
	require.NoError(t, err)
	require.False(t, truncated)
	require.Len(t, points, 2)

	var stdout, stderr bytes.Buffer
	require.NoError(t, runIssueCheckpointListJSON(ctx, &stdout, &stderr, "", "PROJ-1", 1))
	var entries []branchCheckpointJSON
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &entries))
	require.Equal(t, []string{"b2b2b2b2b2b2"}, checkpointIDs(entries))
	require.Contains(t, stderr.String(), "capped")
}

func TestCheckpointListCmd_IssueWithPendingErrors(t *testing.T) {
	setupCheckpointListRepo(t)

	cmd := newCheckpointListCmd()
	cmd.SetArgs([]string{"--pending", "--issue", "PROJ-1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "--issue cannot be combined with --pending")
}

func TestIsWalkableBranch(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"refs/heads/main":                           true,
		"refs/heads/feat/PROJ-1":                    true,
		"refs/heads/entire/checkpoints/v1":          false,
		"refs/heads/entire/abc1234-def":             false,
		"refs/remotes/origin/feat/PROJ-1":           true,
		"refs/remotes/origin/HEAD":                  false,
		"refs/remotes/origin/entire/checkpoints/v1": false,
		"refs/tags/v1":                              false,
		"refs/entire/checkpoints/a1/b2":             false,
	}
	for name, want := range cases {
		if got := isWalkableBranch(plumbing.ReferenceName(name)); got != want {
			t.Errorf("isWalkableBranch(%s) = %v, want %v", name, got, want)
		}
	}
}

func TestLinkedIssueKey(t *testing.T) {
	t.Parallel()

	info := checkpoint.CheckpointInfo{IssueKey: "META-1"}
	require.Equal(t, "TRAILER-2", linkedIssueKey(&object.Commit{Message: "x\n\nEntire-Issue: TRAILER-2\n"}, info))
	require.Equal(t, "META-1", linkedIssueKey(&object.Commit{Message: "x\n"}, info))
	// A key quoted in a squash body is not the commit's own.
	require.Equal(t, "META-1", linkedIssueKey(&object.Commit{Message: "squash\n\n    Entire-Issue: OTHER-3\n\nmore\n"}, info))
}
