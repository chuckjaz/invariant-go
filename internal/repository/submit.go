package repository

import (
	"context"
	"fmt"
	"os"

	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// SubmitOptions specifies parameters for submitting a change workspace.
type SubmitOptions struct {
	WorkspaceDir string
	TargetBranch string
	AuthorName   string
}

// ExecuteSubmit submits the active change branch to its upstream branch and retires the workspace.
func ExecuteSubmit(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	opts SubmitOptions,
) (*commit.SubmitResponse, error) {
	_, meta, err := FindWorkspaceRoot(opts.WorkspaceDir)
	if err != nil {
		return nil, err
	}

	if meta.BranchName == "main" {
		return nil, fmt.Errorf("cannot submit main branch into itself")
	}

	target := opts.TargetBranch
	if target == "" {
		target = meta.Upstream
	}
	if target == "" {
		target = "main"
	}

	author := Identity{Name: opts.AuthorName}
	if author.Name == "" {
		author = CurrentIdentity(ctx)
	}

	req := commit.SubmitRequest{
		RepoName:     meta.RepoName,
		ChangeBranch: meta.BranchName,
		TargetBranch: target,
		Author:       author,
	}

	resp, err := commitSvc.SubmitChange(ctx, req)
	if err != nil {
		return resp, fmt.Errorf("submit failed: %w", err)
	}

	// Retire change branch workspace
	_ = os.RemoveAll(meta.WorkspaceDir)

	return resp, nil
}
