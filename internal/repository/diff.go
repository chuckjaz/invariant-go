package repository

import (
	"context"
	"fmt"

	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// DiffOptions specifies parameters for computing diffs.
type DiffOptions struct {
	WorkspaceDir string
	Commit1      string
	Commit2      string
	StatOnly     bool
}

// GetDiff computes a unified diff and diff statistics for the workspace or between commits.
func GetDiff(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	commitSvc commit.Service,
	opts DiffOptions,
) (string, commit.DiffStat, error) {
	// Case 1: Two explicit commits
	if opts.Commit1 != "" && opts.Commit2 != "" {
		c1, err := commitSvc.GetCommit(ctx, opts.Commit1)
		if err != nil {
			return "", commit.DiffStat{}, fmt.Errorf("failed to read commit %s: %w", opts.Commit1, err)
		}
		c2, err := commitSvc.GetCommit(ctx, opts.Commit2)
		if err != nil {
			return "", commit.DiffStat{}, fmt.Errorf("failed to read commit %s: %w", opts.Commit2, err)
		}
		return commitSvc.ComputeDiff(ctx, c1.Tree, c2.Tree)
	}

	// Case 2: Diff workspace against a specific commit or HEAD
	_, meta, err := FindWorkspaceRoot(opts.WorkspaceDir)
	if err != nil {
		return "", commit.DiffStat{}, err
	}

	targetCommitHash := opts.Commit1
	if targetCommitHash == "" {
		h, err := slotsClient.Get(ctx, meta.SlotID)
		if err != nil {
			h = meta.CommitHash
		}
		targetCommitHash = h
	}

	targetCommit, err := commitSvc.GetCommit(ctx, targetCommitHash)
	if err != nil {
		return "", commit.DiffStat{}, fmt.Errorf("failed to read commit %s: %w", targetCommitHash, err)
	}

	// Snapshot active workspace directory in memory
	activeTreeLink, err := SnapshotDirectory(ctx, meta.WorkspaceDir, store)
	if err != nil {
		return "", commit.DiffStat{}, fmt.Errorf("failed to snapshot workspace %s: %w", meta.WorkspaceDir, err)
	}

	return commitSvc.ComputeDiff(ctx, targetCommit.Tree, activeTreeLink)
}
