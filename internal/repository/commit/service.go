// Package commit provides interfaces, data types, and engine primitives
// for managing immutable commits, branch slots, history graphs, and merges.
package commit

import (
	"context"

	"invariant/internal/content"
	"invariant/internal/repository"
)

// CreateRequest specifies parameters to create an immutable commit.
type CreateRequest struct {
	RepoName   string              `json:"repoName"`
	BranchName string              `json:"branchName"`
	TreeLink   content.ContentLink `json:"treeLink"`
	Parents    []string            `json:"parents"`
	Message    string              `json:"message"`
	Author     repository.Identity `json:"author"`
	Committer  repository.Identity `json:"committer,omitempty"`
	Tags       map[string]string   `json:"tags,omitempty"`
	Refs       map[string]string   `json:"refs,omitempty"`
}

// SubmitRequest specifies parameters for submitting a change set to an upstream branch.
type SubmitRequest struct {
	RepoName       string              `json:"repoName"`
	ChangeBranch   string              `json:"changeBranch"`
	TargetBranch   string              `json:"targetBranch"`
	ExpectedTarget string              `json:"expectedTarget,omitempty"`
	Author         repository.Identity `json:"author"`
}

// SubmitResponse returns the result of a submitted change.
type SubmitResponse struct {
	NewHeadCommit string   `json:"newHeadCommit"`
	Squashed      bool     `json:"squashed"`
	Conflicts     []string `json:"conflicts,omitempty"`
}

// BlameLine captures line-by-line commit attribution.
type BlameLine struct {
	LineNumber int                 `json:"lineNumber"`
	Content    string              `json:"content"`
	CommitHash string              `json:"commitHash"`
	Author     repository.Identity `json:"author"`
	Timestamp  int64               `json:"timestamp"`
}

// DiffStat summarizes the count of files changed and lines inserted or deleted.
type DiffStat struct {
	FilesChanged int      `json:"filesChanged"`
	Insertions   int      `json:"insertions"`
	Deletions    int      `json:"deletions"`
	Details      []string `json:"details,omitempty"`
}

// RebaseActionType specifies the action type in an interactive rebase plan.
type RebaseActionType string

const (
	RebasePick   RebaseActionType = "pick"
	RebaseSquash RebaseActionType = "squash"
	RebaseEdit   RebaseActionType = "edit"
	RebaseDrop   RebaseActionType = "drop"
	RebaseReword RebaseActionType = "reword"
)

// RebaseAction specifies an individual operation in an interactive rebase plan.
type RebaseAction struct {
	Type       RebaseActionType `json:"type"`
	CommitHash string           `json:"commitHash"`
	NewMessage string           `json:"newMessage,omitempty"`
}

// Service defines the interface for repository commit management,
// history querying, diff computation, sync, submit, blame, and bisect.
type Service interface {
	// GetCommit retrieves a commit by its SHA256 hash.
	GetCommit(ctx context.Context, commitHash string) (*repository.Commit, error)

	// CreateCommit creates an immutable commit in CAS and updates the branch slot via CAS.
	CreateCommit(ctx context.Context, req CreateRequest) (*repository.Commit, string, error)

	// GetHistory returns commit history along the first-parent spine or full DAG, optionally filtered by path.
	GetHistory(ctx context.Context, headHash string, spineOnly bool, pathFilter string) ([]*repository.Commit, []string, error)

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
