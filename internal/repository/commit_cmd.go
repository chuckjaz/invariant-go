package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// CommitOptions specifies parameters for committing changes in a workspace.
type CommitOptions struct {
	WorkspaceDir string
	Message      string
	Messages     []string
	Amend        bool
	SquashTarget string
	Tags         map[string]string
	AuthorName   string
}

// ExecuteCommit creates a commit from the active workspace working tree and updates the branch slot.
func ExecuteCommit(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	commitSvc commit.Service,
	opts CommitOptions,
) (*Commit, string, error) {
	_, meta, err := FindWorkspaceRoot(opts.WorkspaceDir)
	if err != nil {
		return nil, "", err
	}

	if !meta.Writable {
		return nil, "", fmt.Errorf("cannot commit to read-only workspace (use 'ir change <name>' to create a writable change branch)")
	}

	// 1. Build commit message
	var msgParts []string
	if opts.Message != "" {
		msgParts = append(msgParts, opts.Message)
	}
	for _, m := range opts.Messages {
		if m != "" {
			msgParts = append(msgParts, m)
		}
	}
	msg := strings.Join(msgParts, "\n\n")
	if msg == "" && !opts.Amend {
		return nil, "", fmt.Errorf("commit message cannot be empty (use -m <msg>)")
	}

	// 2. Snapshot current workspace directory
	treeLink, err := SnapshotDirectory(ctx, meta.WorkspaceDir, store)
	if err != nil {
		return nil, "", fmt.Errorf("failed to snapshot workspace tree: %w", err)
	}

	// 3. Read current HEAD commit
	headCommitHash, err := slotsClient.Get(ctx, meta.SlotID)
	if err != nil {
		headCommitHash = meta.CommitHash
	}

	var parents []string
	refs := make(map[string]string)

	if opts.Amend && headCommitHash != "" {
		headCommit, err := commitSvc.GetCommit(ctx, headCommitHash)
		if err != nil {
			return nil, "", fmt.Errorf("failed to read commit to amend %s: %w", headCommitHash, err)
		}
		parents = headCommit.Parents
		if msg == "" {
			msg = headCommit.Message
		}
		refs["supersedes"] = headCommitHash
	} else if headCommitHash != "" {
		parents = []string{headCommitHash}
	}

	author := Identity{Name: opts.AuthorName}
	if author.Name == "" {
		author = CurrentIdentity(ctx)
	}

	// 4. Create commit via commit.Service
	req := commit.CreateRequest{
		RepoName:   meta.RepoName,
		BranchName: meta.BranchName,
		TreeLink:   treeLink,
		Parents:    parents,
		Message:    msg,
		Author:     author,
		Tags:       opts.Tags,
		Refs:       refs,
	}

	c, newCommitHash, err := commitSvc.CreateCommit(ctx, req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create commit: %w", err)
	}

	// 5. Update local workspace metadata
	meta.CommitHash = newCommitHash
	metaData, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(meta.WorkspaceDir, ".invariant-workspace"), metaData, 0644)

	return c, newCommitHash, nil
}
