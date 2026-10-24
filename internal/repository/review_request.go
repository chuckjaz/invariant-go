package repository

import (
	"context"
	"fmt"

	"github.com/chuckjaz/invariant-go/internal/names"
	"github.com/chuckjaz/invariant-go/internal/repository/commit"
	"github.com/chuckjaz/invariant-go/internal/repository/review"
	"github.com/chuckjaz/invariant-go/internal/slots"
	"github.com/chuckjaz/invariant-go/internal/storage"
)

// RequestReviewOptions specifies parameters for requesting a review.
type RequestReviewOptions struct {
	WorkspaceDir string
	AuthorName   string
}

// RequestReview initiates a code review for the current workspace change branch.
func RequestReview(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	reviewSvc review.Service,
	opts RequestReviewOptions,
) (*review.Record, string, error) {
	_, meta, err := FindWorkspaceRoot(opts.WorkspaceDir)
	if err != nil {
		return nil, "", err
	}

	if meta.BranchName == "main" {
		return nil, "", fmt.Errorf("cannot request review on main branch")
	}

	author := Identity{Name: opts.AuthorName}
	if author.Name == "" {
		author = CurrentIdentity(ctx)
	}

	rec, err := reviewSvc.RequestReview(ctx, meta.RepoName, meta.BranchName, author)
	if err != nil {
		return nil, "", fmt.Errorf("failed to request review: %w", err)
	}

	// Tag the change branch HEAD commit with Tags["review"] = rec.Token if possible
	if meta.SlotID != "" && slotsClient != nil {
		currCommitHash, err := slotsClient.Get(ctx, meta.SlotID)
		if err == nil && currCommitHash != "" {
			c, err := commitSvc.GetCommit(ctx, currCommitHash)
			if err == nil && c != nil {
				if c.Tags == nil {
					c.Tags = make(map[string]string)
				}
				c.Tags["review"] = rec.Token
			}
			if localRev, ok := reviewSvc.(*review.LocalService); ok {
				localRev.AssociateCommit(rec.Token, currCommitHash)
			}
		}
	}

	reviewURL := fmt.Sprintf("https://invariant.dev/reviews/%s", rec.Token)
	return rec, reviewURL, nil
}
