package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/repository/review"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// OpenReviewOptions specifies parameters for opening a review workspace.
type OpenReviewOptions struct {
	Identifier   string
	TargetDir    string
	Writable     bool
	WorkspaceDir string
}

// OpenReview opens a review workspace for read-only viewing (or writable suggestion side-branch) without mutating review state.
func OpenReview(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	reviewSvc review.Service,
	opts OpenReviewOptions,
) (string, *review.Record, error) {
	if opts.Identifier == "" {
		return "", nil, fmt.Errorf("missing review identifier (token, commit SHA, or branch name)")
	}

	rec, err := reviewSvc.GetReview(ctx, opts.Identifier)
	if err != nil {
		return "", nil, fmt.Errorf("could not find review for identifier %q: %w", opts.Identifier, err)
	}

	targetDir := opts.TargetDir
	if targetDir == "" {
		cwd := opts.WorkspaceDir
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		wsRoot, _, err := FindWorkspaceRoot(cwd)
		if err == nil {
			targetDir = filepath.Join(filepath.Dir(wsRoot), "review", rec.Token)
		} else {
			targetDir = filepath.Join(cwd, rec.Token)
		}
	}

	absDir, err := filepath.Abs(targetDir)
	if err != nil {
		return "", nil, err
	}

	if err := os.MkdirAll(absDir, 0755); err != nil {
		return "", nil, fmt.Errorf("failed to create review workspace directory %s: %w", absDir, err)
	}

	meta := &WorkspaceMetadata{
		RepoName:     rec.RepoName,
		BranchName:   rec.BranchName,
		CommitHash:   rec.HeadCommit,
		SlotID:       rec.ChangeSlotID,
		WorkspaceDir: absDir,
	}

	if err := WriteWorkspaceMetadata(absDir, meta); err != nil {
		return "", nil, fmt.Errorf("failed to write review workspace metadata: %w", err)
	}

	_ = ChangeWorkingDirectory(absDir)
	return absDir, rec, nil
}
