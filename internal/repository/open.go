package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// OpenOptions specifies parameters for opening an existing repository workspace.
type OpenOptions struct {
	RepoName  string
	Branch    string // Target branch (default: "main")
	TargetDir string // Target root directory on disk (default: ./<RepoName>)
	Writable  bool
}

// OpenRepository opens an existing repository from Names service, materializes the branch file tree,
// writes .invariant-workspace metadata, and changes the working directory to the branch workspace.
func OpenRepository(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	opts OpenOptions,
) (string, error) {
	if opts.RepoName == "" {
		return "", fmt.Errorf("repository name cannot be empty")
	}

	branchName := opts.Branch
	if branchName == "" {
		branchName = "main"
	}

	// 1. Resolve branch slot in Names service
	var slotID string
	if branchName == "main" {
		entry, err := namesClient.Get(ctx, opts.RepoName)
		if err == nil && entry.Value != "" {
			slotID = entry.Value
		} else {
			entryMain, errMain := namesClient.Get(ctx, opts.RepoName+":main")
			if errMain == nil && entryMain.Value != "" {
				slotID = entryMain.Value
			}
		}
	} else {
		entry, err := namesClient.Get(ctx, opts.RepoName+":"+branchName)
		if err == nil && entry.Value != "" {
			slotID = entry.Value
		}
	}

	if slotID == "" {
		return "", fmt.Errorf("repository %q (branch %q) not found in names service", opts.RepoName, branchName)
	}

	// 2. Read latest commit hash from branch slot
	commitHash, err := slotsClient.Get(ctx, slotID)
	if err != nil || commitHash == "" {
		return "", fmt.Errorf("failed to retrieve commit from branch slot %s: %w", slotID, err)
	}

	// 3. Create target workspace directory
	targetRoot := opts.TargetDir
	if targetRoot == "" {
		targetRoot = opts.RepoName
	}
	branchDir := filepath.Join(targetRoot, branchName)
	if err := os.MkdirAll(branchDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create workspace directory %s: %w", branchDir, err)
	}

	// 4. Materialize tree files in workspace
	commitObj, err := commitSvc.GetCommit(ctx, commitHash)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve commit object %s: %w", commitHash, err)
	}
	if err := MaterializeTree(ctx, commitObj.Tree, branchDir, store); err != nil {
		return "", fmt.Errorf("failed to materialize tree in %s: %w", branchDir, err)
	}

	// 5. Write workspace metadata
	meta := &WorkspaceMetadata{
		RepoName:     opts.RepoName,
		BranchName:   branchName,
		Upstream:     branchName,
		SlotID:       slotID,
		CommitHash:   commitHash,
		Writable:     opts.Writable,
		CreatedAt:    time.Now().Unix(),
		WorkspaceDir: branchDir,
	}
	if err := WriteWorkspaceMetadata(branchDir, meta); err != nil {
		return "", err
	}

	_ = ChangeWorkingDirectory(branchDir)
	return branchDir, nil
}
