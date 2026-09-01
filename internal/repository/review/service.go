// Package review provides interfaces, data models, and services
// for managing code review lifecycles, comment threads, tokens, and approvals.
package review

import (
	"context"

	"invariant/internal/repository/identity"
)

// Comment represents a single comment entry in a review thread.
type Comment struct {
	Comment string `json:"comment"`
	Author  string `json:"author,omitempty"`
	Branch  string `json:"branch,omitempty"`
}

// ReviewComment represents a targeted review comment thread on a file or line range.
type ReviewComment struct {
	Comments  []Comment `json:"comments"`
	File      string    `json:"file,omitempty"`
	Offset    *int      `json:"offset,omitempty"`
	Len       *int      `json:"len,omitempty"`
	StartLine *int      `json:"startLine,omitempty"`
	EndLine   *int      `json:"endLine,omitempty"`
	Resolved  bool      `json:"resolved,omitempty"`
}

// Status represents the lifecycle state of a code review.
type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusApproved   Status = "approved"
	StatusRejected   Status = "rejected"
	StatusAbandoned  Status = "abandoned"
)

// Record holds the complete metadata and comment threads for a code review.
type Record struct {
	Token        string          `json:"token"`
	RepoName     string          `json:"repoName"`
	BranchName   string          `json:"branchName"`
	ChangeSlotID string          `json:"changeSlotId"`
	BaseCommit   string          `json:"baseCommit"`
	HeadCommit   string          `json:"headCommit"`
	Status       Status          `json:"status"`
	Reviewer     string          `json:"reviewer,omitempty"`
	Comments     []ReviewComment `json:"comments,omitempty"`
	CreatedAt    int64           `json:"createdAt"`
	UpdatedAt    int64           `json:"updatedAt"`
}

// Service defines the interface for managing code reviews, tokens, and comment threads.
type Service interface {
	// RequestReview creates a review record for a change branch and emits a unique review token (StatusPending).
	RequestReview(ctx context.Context, repoName, branchName string, author identity.Identity) (*Record, error)

	// GetReview retrieves review metadata and comment threads by token, commit hash, or branch name without altering review state.
	// Used by 'ir review open' to view reviews in any state (pending, in-progress, or closed).
	GetReview(ctx context.Context, identifier string) (*Record, error)

	// StartReview officially starts a review and transitions its state to StatusInProgress.
	StartReview(ctx context.Context, token string, reviewer identity.Identity) error

	// AddComments appends or updates structured comments on a review.
	AddComments(ctx context.Context, token string, comments []ReviewComment, author identity.Identity) error

	// ApproveReview marks the review as approved.
	ApproveReview(ctx context.Context, token string, reviewer identity.Identity) error

	// RejectReview marks the review as rejected.
	RejectReview(ctx context.Context, token string, reviewer identity.Identity) error

	// AbandonReview marks the review as abandoned.
	AbandonReview(ctx context.Context, token string, author identity.Identity) error
}
