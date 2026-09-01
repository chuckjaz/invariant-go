package repository

import (
	"context"
	"fmt"
	"os"

	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/repository/review"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// StartReviewOptions specifies parameters for starting a code review.
type StartReviewOptions struct {
	Identifier   string
	TargetDir    string
	Writable     bool
	WorkspaceDir string
	ReviewerName string
}

// StartReview marks a review as in-progress and creates/opens the review workspace directory if not already opened.
func StartReview(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	reviewSvc review.Service,
	opts StartReviewOptions,
) (string, *review.Record, error) {
	if opts.Identifier == "" {
		cwd := opts.WorkspaceDir
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		_, meta, err := FindWorkspaceRoot(cwd)
		if err == nil {
			opts.Identifier = meta.BranchName
		}
	}

	if opts.Identifier == "" {
		return "", nil, fmt.Errorf("missing review identifier (token, commit SHA, or branch name)")
	}

	rec, err := reviewSvc.GetReview(ctx, opts.Identifier)
	if err != nil {
		return "", nil, fmt.Errorf("could not find review for identifier %q: %w", opts.Identifier, err)
	}

	reviewer := Identity{Name: opts.ReviewerName}
	if reviewer.Name == "" {
		reviewer = CurrentIdentity(ctx)
	}

	if err := reviewSvc.StartReview(ctx, rec.Token, reviewer); err != nil {
		return "", nil, fmt.Errorf("failed to start review %s: %w", rec.Token, err)
	}

	rec.Status = review.StatusInProgress
	rec.Reviewer = reviewer.Name

	wsDir, _, err := OpenReview(ctx, store, slotsClient, namesClient, commitSvc, reviewSvc, OpenReviewOptions{
		Identifier:   rec.Token,
		TargetDir:    opts.TargetDir,
		Writable:     opts.Writable,
		WorkspaceDir: opts.WorkspaceDir,
	})
	if err != nil {
		return "", nil, err
	}

	return wsDir, rec, nil
}
