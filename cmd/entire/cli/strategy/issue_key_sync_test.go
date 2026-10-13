package strategy

import (
	"context"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint"
	"github.com/entireio/cli/cmd/entire/cli/checkpoint/id"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/testutil"
	"github.com/entireio/cli/redact"

	"github.com/go-git/go-git/v6/plumbing"
)

// syncIssueKeyMetadata must work through the store abstraction for both
// backends: an amend that edits or removes the Entire-Issue trailer rewrites
// the issue_key of every session of the kept checkpoint.
// Uses t.Chdir — do NOT add t.Parallel().
func TestSyncIssueKeyMetadata(t *testing.T) {
	backends := map[string]string{
		"git-branch": `{"enabled": true, "issue_key_pattern": "[A-Z]+-\\d+"}`,
		"git-refs":   `{"enabled": true, "issue_key_pattern": "[A-Z]+-\\d+", "checkpoints": {"primary": {"type": "git-refs"}}}`,
	}
	for name, settingsJSON := range backends {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			testutil.InitRepo(t, dir)
			testutil.WriteFile(t, dir, ".entire/settings.json", settingsJSON)
			testutil.WriteFile(t, dir, "a.txt", "a")
			testutil.GitAdd(t, dir, "a.txt")
			testutil.GitCommit(t, dir, "initial")
			t.Chdir(dir)

			ctx := context.Background()
			repo, err := OpenRepository(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer repo.Close()

			s := NewManualCommitStrategy()
			store, err := s.getPersistentStore(ctx, repo)
			if err != nil {
				t.Fatal(err)
			}
			cpID := id.MustCheckpointID("a1b2c3d4e5f6")
			for _, sessionID := range []string{"sess-1", "sess-2"} {
				if err := store.Write(ctx, checkpoint.Session(checkpoint.WriteOptions{
					CheckpointID: cpID, SessionID: sessionID, Strategy: "manual-commit",
					Transcript: redact.AlreadyRedacted([]byte("{}\n")),
					AuthorName: "Test", AuthorEmail: "test@example.com",
					IssueKey: "OLD-1",
				})); err != nil {
					t.Fatalf("write session %s: %v", sessionID, err)
				}
			}

			assertKeys := func(want string) {
				t.Helper()
				for i := range 2 {
					meta, err := store.ReadSessionMetadata(ctx, cpID, i)
					if err != nil {
						t.Fatalf("read session %d: %v", i, err)
					}
					if meta.IssueKey != want {
						t.Fatalf("session %d issue_key = %q, want %q", i, meta.IssueKey, want)
					}
				}
			}
			assertKeys("OLD-1")
			_, v1Err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
			if (v1Err == nil) != (name == "git-branch") {
				t.Fatalf("v1 branch presence = %v, want only for the git-branch backend", v1Err == nil)
			}

			s.syncIssueKeyMetadata(ctx, repo, cpID, "NEW-2")
			assertKeys("NEW-2")

			infos, err := store.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(infos) != 1 {
				t.Fatalf("List() = %d checkpoints, want 1", len(infos))
			}

			s.syncIssueKeyMetadata(ctx, repo, cpID, "")
			assertKeys("")
		})
	}
}
