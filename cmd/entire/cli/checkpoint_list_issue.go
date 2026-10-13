package cli

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"

	"github.com/entireio/cli/cmd/entire/cli/checkpoint"
	"github.com/entireio/cli/cmd/entire/cli/checkpoint/id"
	"github.com/entireio/cli/cmd/entire/cli/logging"
	"github.com/entireio/cli/cmd/entire/cli/strategy"
	"github.com/entireio/cli/cmd/entire/cli/trailers"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// issueCommitScanLimit bounds the commits `checkpoint list --issue` reads
// across all branches looking for Entire-Issue trailers. The checkpoint
// store's issue_key metadata is searched in full regardless; the trailer
// scan only refines it (a trailer wins over metadata for its checkpoint) and
// supplies commit SHAs and subjects.
const issueCommitScanLimit = 5000

// linkedIssueKey is the issue key linking checkpoint info to commit c: the
// commit's Entire-Issue trailer when it has one (the durable, user-visible
// link), else the checkpoint metadata's issue_key.
func linkedIssueKey(c *object.Commit, info checkpoint.CheckpointInfo) string {
	if key, ok := trailers.ParseIssue(c.Message); ok {
		return key
	}
	return info.IssueKey
}

// getIssueCheckpoints returns the checkpoints linked to issueKey
// (case-insensitive exact match) across the whole repository, newest first,
// at most limit of them; the bool reports that more were dropped (or the
// commit scan hit its budget).
//
// Two sources, so a checkpoint stays findable after its branch is merged or
// deleted:
//   - commits reachable from any local or remote-tracking branch (Entire's
//     own refs excluded) that carry an Entire-Checkpoint trailer: such a
//     checkpoint matches by linkedIssueKey, one entry per commit;
//   - every other checkpoint in the store: matches by its metadata issue_key
//     (its commit is gone or unreachable, e.g. a deleted branch or a squash
//     merge that dropped the trailers). These entries carry no commit.
//
// Remote-discovered checkpoints not yet hydrated have no metadata until
// after the limit, so they match only through a reachable commit's trailer.
func getIssueCheckpoints(ctx context.Context, repo *git.Repository, limit int, issueKey string) ([]strategy.PendingCheckpoint, bool, error) {
	strategy.WarnIfMetadataDisconnected(ctx)

	// Same store envelope as getBranchCheckpoints (see there).
	stores, err := checkpoint.Open(ctx, repo, checkpoint.OpenOptions{
		BlobFetcher:     FetchBlobsByHash,
		RefFetcher:      FetchCheckpointRef,
		RemoteRefLister: ListCheckpointRefsOnRemote,
		ReadRemotes:     strategy.CheckpointReadRemotes(ctx),
	})
	if err != nil {
		return nil, false, fmt.Errorf("open checkpoint store: %w", err)
	}
	store := stores.Persistent

	infos, err := store.List(checkpoint.WithRemoteListDiscovery(ctx))
	if err != nil {
		infos = nil // Continue with trailer matches only
	}
	committedByID := make(map[id.CheckpointID]checkpoint.CheckpointInfo, len(infos))
	for _, info := range infos {
		if !info.CheckpointID.IsEmpty() {
			committedByID[info.CheckpointID] = info
		}
	}

	matches := func(key string) bool { return key != "" && strings.EqualFold(key, issueKey) }
	var points []strategy.PendingCheckpoint
	onReachableCommit := make(map[id.CheckpointID]bool)

	scanTruncated, err := walkBranchCommits(ctx, repo, issueCommitScanLimit, func(c *object.Commit) {
		cpID, found := trailers.ParseCheckpoint(c.Message)
		if !found {
			return
		}
		info, found := committedByID[cpID]
		if !found {
			return
		}
		onReachableCommit[cpID] = true
		key := linkedIssueKey(c, info)
		if !matches(key) {
			return
		}
		points = append(points, strategy.PendingCheckpoint{
			ID:               c.Hash.String(),
			Message:          strings.Split(c.Message, "\n")[0],
			Date:             c.Committer.When,
			IsLogsOnly:       true,
			CheckpointID:     cpID,
			SessionID:        info.SessionID,
			SessionCount:     info.SessionCount,
			SessionIDs:       info.SessionIDs,
			IsTaskCheckpoint: info.IsTask,
			ToolUseID:        info.ToolUseID,
			Agent:            info.Agent,
			IssueKey:         key,
		})
	})
	if err != nil {
		return nil, false, fmt.Errorf("error iterating commits: %w", err)
	}

	for _, info := range infos {
		if onReachableCommit[info.CheckpointID] || !matches(info.IssueKey) {
			continue
		}
		points = append(points, strategy.PendingCheckpoint{
			// No reachable commit: shown under its checkpoint ID, like an
			// imported checkpoint.
			ID:               info.CheckpointID.String(),
			Date:             info.CreatedAt,
			IsLogsOnly:       true,
			Imported:         info.Imported,
			CheckpointID:     info.CheckpointID,
			SessionID:        info.SessionID,
			SessionCount:     info.SessionCount,
			SessionIDs:       info.SessionIDs,
			IsTaskCheckpoint: info.IsTask,
			ToolUseID:        info.ToolUseID,
			Agent:            info.Agent,
			IssueKey:         info.IssueKey,
		})
	}

	sort.SliceStable(points, func(i, j int) bool {
		return points[i].Date.After(points[j].Date)
	})
	truncated := scanTruncated
	if len(points) > limit {
		points = points[:limit]
		truncated = true
	}

	hydrateListedBranchCheckpoints(ctx, store, points, committedByID)
	for i := range points {
		info := committedByID[points[i].CheckpointID]
		if info.ListedStub || points[i].SessionPrompt != "" {
			continue
		}
		points[i].SessionPrompt = readLatestCommittedSessionPrompt(ctx, store, points[i].CheckpointID, info.SessionCount)
		if points[i].Message == "" {
			points[i].Message = points[i].SessionPrompt
		}
	}
	return points, truncated, nil
}

// walkBranchCommits calls fn for each commit reachable from HEAD and from the
// local and remote-tracking branches, newest committer date first, visiting
// each commit once and at most budget commits. Entire's own branches
// (entire/..., e.g. the checkpoint metadata branch) are not walked. Reports
// whether the budget stopped the walk early.
func walkBranchCommits(ctx context.Context, repo *git.Repository, budget int, fn func(*object.Commit)) (bool, error) {
	queue := &commitQueue{}
	seen := make(map[plumbing.Hash]bool)
	push := func(h plumbing.Hash) {
		if seen[h] {
			return
		}
		seen[h] = true
		c, err := repo.CommitObject(h)
		if err != nil {
			return // a tag of a tree, a missing object in a shallow clone, ...
		}
		heap.Push(queue, c)
	}

	if head, err := repo.Head(); err == nil {
		push(head.Hash())
	}
	refs, err := repo.References()
	if err != nil {
		return false, fmt.Errorf("list references: %w", err)
	}
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.SymbolicReference || !isWalkableBranch(ref.Name()) {
			return nil
		}
		push(ref.Hash())
		return nil
	})
	refs.Close()
	if err != nil {
		return false, fmt.Errorf("list references: %w", err)
	}

	for visited := 0; queue.Len() > 0; visited++ {
		if err := ctx.Err(); err != nil {
			return false, err //nolint:wrapcheck // Propagating context cancellation
		}
		if visited >= budget {
			logging.Debug(ctx, "checkpoint list --issue: commit scan budget reached", slog.Int("budget", budget))
			return true, nil
		}
		c, ok := heap.Pop(queue).(*object.Commit)
		if !ok {
			return false, errors.New("commit queue held a non-commit")
		}
		fn(c)
		for _, parent := range c.ParentHashes {
			push(parent)
		}
	}
	return false, nil
}

// isWalkableBranch reports whether name is a local or remote-tracking branch
// that is not one of Entire's own (entire/checkpoints/v1, legacy shadow
// branches), whose commits are metadata, not user history.
func isWalkableBranch(name plumbing.ReferenceName) bool {
	switch {
	case name.IsBranch():
		return !strings.HasPrefix(name.Short(), "entire/")
	case name.IsRemote():
		_, branch, ok := strings.Cut(strings.TrimPrefix(name.String(), "refs/remotes/"), "/")
		return ok && branch != "HEAD" && !strings.HasPrefix(branch, "entire/")
	default:
		return false
	}
}

// commitQueue is a max-heap of commits by committer date.
type commitQueue []*object.Commit

func (q *commitQueue) Len() int { return len(*q) }
func (q *commitQueue) Less(i, j int) bool {
	return (*q)[i].Committer.When.After((*q)[j].Committer.When)
}
func (q *commitQueue) Swap(i, j int) { (*q)[i], (*q)[j] = (*q)[j], (*q)[i] }
func (q *commitQueue) Push(x any) {
	if c, ok := x.(*object.Commit); ok {
		*q = append(*q, c)
	}
}

func (q *commitQueue) Pop() any {
	old := *q
	c := old[len(old)-1]
	*q = old[:len(old)-1]
	return c
}

// runIssueCheckpointList is the human view of `checkpoint list --issue`.
func runIssueCheckpointList(ctx context.Context, w, errW io.Writer, noPager bool, sessionFilter, issueKey string) error {
	repo, err := openRepository(ctx)
	if err != nil {
		return fmt.Errorf("not a git repository: %w", err)
	}
	defer repo.Close()

	points, truncated, err := getIssueCheckpoints(ctx, repo, branchCheckpointsLimit, issueKey)
	if err != nil {
		if ctx.Err() != nil {
			return NewSilentError(ctx.Err())
		}
		return fmt.Errorf("failed to list checkpoints: %w", err)
	}

	header := []explainRow{
		{Label: "issue", Value: issueKey},
		{Label: "scope", Value: "all branches"},
	}
	output := formatCheckpointList(w, header, points, sessionFilter,
		"No checkpoints linked to "+issueKey+".\n"+
			"Checkpoints are linked when the project sets issue_key_pattern and the branch name or commit subject matches it.\n")
	outputExplainContent(w, output, noPager)

	if truncated {
		fmt.Fprintf(errW, "note: showing the newest %d matches; older checkpoints may be hidden.\n", branchCheckpointsLimit)
	}
	return nil
}

// runIssueCheckpointListJSON is `checkpoint list --issue --json`: the same
// flat array as the branch list.
func runIssueCheckpointListJSON(ctx context.Context, w, errW io.Writer, sessionFilter, issueKey string, listLimit int) error {
	repo, err := openRepository(ctx)
	if err != nil {
		return fmt.Errorf("not a git repository: %w", err)
	}
	defer repo.Close()

	limit := listLimit
	if limit <= 0 {
		limit = branchCheckpointsLimit
	}
	points, truncated, err := getIssueCheckpoints(ctx, repo, limit, issueKey)
	if err != nil {
		if ctx.Err() != nil {
			return NewSilentError(ctx.Err())
		}
		return fmt.Errorf("failed to list checkpoints: %w", err)
	}
	return writeCheckpointListJSON(w, errW, points, sessionFilter, truncated, limit)
}
