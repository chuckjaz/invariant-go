package repository

import (
	"context"
	"fmt"
	"strings"

	"invariant/internal/content"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
	"invariant/internal/workspace"
)

// RevertOptions specifies parameters for reverting a commit.
type RevertOptions struct {
	WorkspaceDir string
	CommitHash   string
	NoCommit     bool
}

// RevertResult holds the result of a revert operation.
type RevertResult struct {
	RevertCommitHash string
	NewCommit        *commit.Commit
	Conflicts        []string
}

// ExecuteRevert applies the inverse patch of targetCommit onto current branch and commits the result.
func ExecuteRevert(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitSvc commit.Service, opts RevertOptions) (*RevertResult, error) {
	wsRoot, meta, err := FindWorkspaceRoot(opts.WorkspaceDir)
	if err != nil {
		return nil, err
	}

	targetHash := strings.TrimSpace(opts.CommitHash)
	if targetHash == "" {
		return nil, fmt.Errorf("revert requires a commit hash to revert")
	}

	targetCommit, err := commitSvc.GetCommit(ctx, targetHash)
	if err != nil {
		return nil, fmt.Errorf("target commit %q not found: %w", targetHash, err)
	}

	// Determine parent of target commit (inverse target)
	var parentTreeAddr string
	if len(targetCommit.Parents) > 0 {
		parentCommit, err := commitSvc.GetCommit(ctx, targetCommit.Parents[0])
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve parent commit of %s: %w", targetHash, err)
		}
		parentTreeAddr = parentCommit.Tree.Address
	} else {
		emptyTreeLink, err := CreateEmptyTree(ctx, store)
		if err != nil {
			return nil, err
		}
		parentTreeAddr = emptyTreeLink.Address
	}

	// Determine current HEAD commit
	currentHeadHash := meta.CommitHash
	if meta.SlotID != "" {
		slotAddr, err := slotsClient.Get(ctx, meta.SlotID)
		if err == nil && slotAddr != "" {
			currentHeadHash = slotAddr
		}
	}

	headCommit, err := commitSvc.GetCommit(ctx, currentHeadHash)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve HEAD commit %s: %w", currentHeadHash, err)
	}

	// 3-way merge applying inverse patch:
	// Ancestor = targetCommit.Tree (where changes were introduced)
	// Ours     = headCommit.Tree (current state)
	// Theirs   = parentTreeAddr (pre-target state, reversing target changes)
	mergedTreeAddr, conflicts, err := workspace.MergeTrees(
		ctx,
		targetCommit.Tree.Address,
		headCommit.Tree.Address,
		parentTreeAddr,
		store,
		slotsClient,
	)
	if err != nil {
		return nil, fmt.Errorf("revert merge calculation failed: %w", err)
	}

	if len(conflicts) > 0 {
		return &RevertResult{
			Conflicts: conflicts,
		}, fmt.Errorf("revert encountered %d conflict(s)", len(conflicts))
	}

	// Materialize changes to workspace disk
	if err := MaterializeTree(ctx, content.ContentLink{Address: mergedTreeAddr}, wsRoot, store); err != nil {
		return nil, fmt.Errorf("failed to update workspace disk: %w", err)
	}

	firstLine := strings.Split(strings.TrimSpace(targetCommit.Message), "\n")[0]
	revertMsg := fmt.Sprintf("Revert %q\n\nThis reverts commit %s.", firstLine, targetHash)

	newCommit, newHash, err := commitSvc.CreateCommit(ctx, commit.CreateRequest{
		RepoName:   meta.RepoName,
		BranchName: meta.BranchName,
		TreeLink:   content.ContentLink{Address: mergedTreeAddr},
		Parents:    []string{currentHeadHash},
		Message:    revertMsg,
		Author:     headCommit.Author,
		Refs: map[string]string{
			"reverts": targetHash,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to record revert commit: %w", err)
	}

	// Update slot and metadata
	if meta.SlotID != "" {
		_ = slotsClient.Update(ctx, meta.SlotID, newHash, currentHeadHash, nil)
	}
	meta.CommitHash = newHash
	_ = WriteWorkspaceMetadata(wsRoot, meta)

	return &RevertResult{
		RevertCommitHash: newHash,
		NewCommit:        newCommit,
	}, nil
}
