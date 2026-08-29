package repository

import (
	"context"

	"invariant/internal/content"
)

// CreateCommitRequest specifies parameters to create an immutable commit.
type CreateCommitRequest struct {
	RepoName   string              `json:"repoName"`
	BranchName string              `json:"branchName"`
	TreeLink   content.ContentLink `json:"treeLink"`
	Parents    []string            `json:"parents"`
	Message    string              `json:"message"`
	Author     Identity            `json:"author"`
	Committer  Identity            `json:"committer"`
	Tags       map[string]string   `json:"tags,omitempty"`
	Refs       map[string]string   `json:"refs,omitempty"`
}

// SubmitRequest specifies parameters for submitting a change set to an upstream branch.
type SubmitRequest struct {
	RepoName       string   `json:"repoName"`
	ChangeBranch   string   `json:"changeBranch"`
	TargetBranch   string   `json:"targetBranch"`
	ExpectedTarget string   `json:"expectedTarget,omitempty"`
	Author         Identity `json:"author"`
}

// SubmitResponse returns the result of a submitted change.
type SubmitResponse struct {
	NewHeadCommit string   `json:"newHeadCommit"`
	Squashed      bool     `json:"squashed"`
	Conflicts     []string `json:"conflicts,omitempty"`
}

// CommitService defines the interface for repository commit management,
// history querying, diff computation, sync, submit, blame, and bisect.
type CommitService interface {
	// GetCommit retrieves a commit by its SHA256 hash.
	GetCommit(ctx context.Context, commitHash string) (*Commit, error)

	// CreateCommit creates an immutable commit in CAS and updates the branch slot via CAS.
	CreateCommit(ctx context.Context, req CreateCommitRequest) (*Commit, string, error)

	// GetHistory returns commit history along the first-parent spine or full DAG, optionally filtered by path.
	GetHistory(ctx context.Context, headHash string, spineOnly bool, pathFilter string) ([]*Commit, []string, error)

	// ComputeDiff calculates unified diff and statistics between two commit trees or workspace against a commit.
	ComputeDiff(ctx context.Context, fromTree, toTree content.ContentLink) (string, DiffStat, error)

	// SyncBranch performs a 3-way rebase of changeBranch onto targetBranch HEAD.
	SyncBranch(ctx context.Context, repoName, changeBranch string) (string, []string, error)

	// AbortSync restores the branch workspace to the pre-sync state.
	AbortSync(ctx context.Context, repoName, changeBranch string) error

	// SubmitChange validates prerequisites, fast-forwards/rebases, and updates target branch slot.
	SubmitChange(ctx context.Context, req SubmitRequest) (*SubmitResponse, error)

	// Blame computes line-by-line commit attribution for a file.
	Blame(ctx context.Context, commitHash, filePath string) ([]BlameLine, error)

	// Bisect computes the next candidate midpoint commit between good and bad commits.
	Bisect(ctx context.Context, goodCommits, badCommits []string) (string, int, error)

	// InteractiveRebase applies an edited commit plan (reorder, squash, edit, drop) onto a base.
	InteractiveRebase(ctx context.Context, repoName, changeBranch, baseCommit string, plan []RebaseAction) (string, error)
}

// ReviewService defines the interface for managing code reviews, tokens, and comment threads.
type ReviewService interface {
	// RequestReview creates a review record for a change branch and emits a unique review token.
	RequestReview(ctx context.Context, repoName, branchName string, author Identity) (*ReviewRecord, error)

	// GetReview retrieves review metadata and comment threads by token, commit hash, or branch name.
	GetReview(ctx context.Context, identifier string) (*ReviewRecord, error)

	// AddComments appends or updates structured comments on a review.
	AddComments(ctx context.Context, token string, comments []ReviewComment, author Identity) error

	// ApproveReview marks the review as approved.
	ApproveReview(ctx context.Context, token string, reviewer Identity) error

	// RejectReview marks the review as rejected.
	RejectReview(ctx context.Context, token string, reviewer Identity) error

	// AbandonReview marks the review as abandoned.
	AbandonReview(ctx context.Context, token string, author Identity) error
}

// ConfigService defines the interface for repository and global configuration properties.
type ConfigService interface {
	// GetConfig retrieves a configuration setting value.
	GetConfig(ctx context.Context, repoName, key string) (string, error)

	// SetConfig sets a configuration setting value.
	SetConfig(ctx context.Context, repoName, key, value string) error

	// ListConfig lists all configuration settings.
	ListConfig(ctx context.Context, repoName string) (map[string]string, error)

	// UnsetConfig removes a configuration setting.
	UnsetConfig(ctx context.Context, repoName, key string) error
}
