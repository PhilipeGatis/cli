//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/testutil"
)

type issueListEntry struct {
	CheckpointID string `json:"checkpoint_id"`
	IssueKey     string `json:"issue_key"`
}

func listByIssue(t *testing.T, env *TestEnv, key string) []issueListEntry {
	t.Helper()
	out := env.RunCLI("checkpoint", "list", "--json", "--no-pager", "--issue", key)
	start := strings.Index(out, "[")
	if start < 0 {
		t.Fatalf("checkpoint list printed no JSON array:\n%s", out)
	}
	var entries []issueListEntry
	if err := json.Unmarshal([]byte(out[start:]), &entries); err != nil {
		t.Fatalf("invalid list JSON: %v\n%s", err, out)
	}
	return entries
}

// TestIssueKey_ListAcrossBranches drives the real hooks and then finds the
// checkpoint with `checkpoint list --issue` from another branch, and again
// after its feature branch is deleted, for both checkpoint backends.
func TestIssueKey_ListAcrossBranches(t *testing.T) {
	t.Parallel()
	ForEachBackend(t, func(t *testing.T, backend string) {
		env := NewRepoWithCommit(t)
		env.CheckpointStore = backend
		mainBranch := strings.TrimSpace(testutil.RunGit(t, env.RepoDir, "rev-parse", "--abbrev-ref", "HEAD"))
		env.GitCheckoutNewBranch("feature/PROJ-77-login-timeout")
		env.PatchSettings(map[string]any{"issue_key_pattern": issueKeyPattern})
		agentTurn(t, env, "login.txt", "timeout fix")
		env.GitCommitWithHooks("Fix login timeout", "login.txt")
		cpID := headCheckpointID(t, env)

		want := []issueListEntry{{CheckpointID: cpID, IssueKey: "PROJ-77"}}
		if got := listByIssue(t, env, "proj-77"); len(got) != 1 || got[0] != want[0] {
			t.Fatalf("on the feature branch: --issue proj-77 = %+v, want %+v", got, want)
		}
		if other := listByIssue(t, env, "PROJ-78"); len(other) != 0 {
			t.Fatalf("--issue PROJ-78 must not match PROJ-77: %+v", other)
		}

		testutil.RunGit(t, env.RepoDir, "checkout", "-q", mainBranch)
		if got := listByIssue(t, env, "PROJ-77"); len(got) != 1 || got[0] != want[0] {
			t.Fatalf("from %s: --issue PROJ-77 = %+v, want %+v", mainBranch, got, want)
		}

		testutil.RunGit(t, env.RepoDir, "branch", "-q", "-D", "feature/PROJ-77-login-timeout")
		if got := listByIssue(t, env, "PROJ-77"); len(got) != 1 || got[0] != want[0] {
			t.Fatalf("after deleting the branch: --issue PROJ-77 = %+v, want %+v", got, want)
		}
	})
}
