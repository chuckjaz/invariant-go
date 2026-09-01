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

// ReviewActionOptions specifies parameters for review actions (approve, reject, abandon, update).
type ReviewActionOptions struct {
	WorkspaceDir string
	Identifier   string
	ReviewerName string
}

// ApproveReview approves a review and closes the review workspace.
func ApproveReview(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	reviewSvc review.Service,
	opts ReviewActionOptions,
) (*review.Record, error) {
	token := opts.Identifier
	var wsDir string
	if token == "" {
		cwd := opts.WorkspaceDir
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		root, meta, err := FindWorkspaceRoot(cwd)
		if err == nil {
			token = meta.BranchName
			wsDir = root
		}
	}

	if token == "" {
		return nil, fmt.Errorf("missing review identifier (token or branch name)")
	}

	reviewer := Identity{Name: opts.ReviewerName}
	if reviewer.Name == "" {
		reviewer = CurrentIdentity(ctx)
	}

	rec, err := reviewSvc.GetReview(ctx, token)
	if err != nil {
		return nil, err
	}

	if err := reviewSvc.ApproveReview(ctx, rec.Token, reviewer); err != nil {
		return nil, fmt.Errorf("failed to approve review: %w", err)
	}
	rec.Status = review.StatusApproved
	rec.Reviewer = reviewer.Name

	if wsDir != "" {
		_ = os.RemoveAll(wsDir)
	}

	return rec, nil
}

// RejectReview marks a review as rejected and closes the review workspace.
func RejectReview(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	reviewSvc review.Service,
	opts ReviewActionOptions,
) (*review.Record, error) {
	token := opts.Identifier
	var wsDir string
	if token == "" {
		cwd := opts.WorkspaceDir
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		root, meta, err := FindWorkspaceRoot(cwd)
		if err == nil {
			token = meta.BranchName
			wsDir = root
		}
	}

	if token == "" {
		return nil, fmt.Errorf("missing review identifier (token or branch name)")
	}

	reviewer := Identity{Name: opts.ReviewerName}
	if reviewer.Name == "" {
		reviewer = CurrentIdentity(ctx)
	}

	rec, err := reviewSvc.GetReview(ctx, token)
	if err != nil {
		return nil, err
	}

	if err := reviewSvc.RejectReview(ctx, rec.Token, reviewer); err != nil {
		return nil, fmt.Errorf("failed to reject review: %w", err)
	}
	rec.Status = review.StatusRejected
	rec.Reviewer = reviewer.Name

	if wsDir != "" {
		_ = os.RemoveAll(wsDir)
	}

	return rec, nil
}

// AbandonReview abandons a review and closes the review workspace.
func AbandonReview(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	reviewSvc review.Service,
	opts ReviewActionOptions,
) (*review.Record, error) {
	token := opts.Identifier
	var wsDir string
	if token == "" {
		cwd := opts.WorkspaceDir
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		root, meta, err := FindWorkspaceRoot(cwd)
		if err == nil {
			token = meta.BranchName
			wsDir = root
		}
	}

	if token == "" {
		return nil, fmt.Errorf("missing review identifier (token or branch name)")
	}

	author := Identity{Name: opts.ReviewerName}
	if author.Name == "" {
		author = CurrentIdentity(ctx)
	}

	rec, err := reviewSvc.GetReview(ctx, token)
	if err != nil {
		return nil, err
	}

	if err := reviewSvc.AbandonReview(ctx, rec.Token, author); err != nil {
		return nil, fmt.Errorf("failed to abandon review: %w", err)
	}
	rec.Status = review.StatusAbandoned

	if wsDir != "" {
		_ = os.RemoveAll(wsDir)
	}

	return rec, nil
}

// UpdateReview updates an existing code review with newly committed changes on the workspace branch.
func UpdateReview(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	reviewSvc review.Service,
	opts ReviewActionOptions,
) (*review.Record, error) {
	cwd := opts.WorkspaceDir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	_, meta, err := FindWorkspaceRoot(cwd)
	if err != nil {
		return nil, err
	}

	token := opts.Identifier
	if token == "" {
		token = meta.BranchName
	}

	rec, err := reviewSvc.GetReview(ctx, token)
	if err != nil {
		return nil, err
	}

	if meta.SlotID != "" && slotsClient != nil {
		currCommitHash, err := slotsClient.Get(ctx, meta.SlotID)
		if err == nil && currCommitHash != "" {
			rec.HeadCommit = currCommitHash
			if localRev, ok := reviewSvc.(*review.LocalService); ok {
				localRev.AssociateCommit(rec.Token, currCommitHash)
			}
		}
	}

	return rec, nil
}
