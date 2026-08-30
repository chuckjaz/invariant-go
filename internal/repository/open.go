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
	"invariant/internal/workspace"
)

// OpenOptions specifies parameters for opening an existing repository FUSE workspace.
type OpenOptions struct {
	RepoName   string
	Branch     string   // Target branch (default: "main")
	TargetDir  string   // Target root directory on disk (default: ./<RepoName>)
	Layers     []string // Additional layers for the FUSE workspace
	Writable   bool
	CreateOnly bool // If true, creates .invariant-workspace configuration without mounting
}

// OpenRepository creates a FUSE workspace for an existing repository from the Names service,
// configures the layered .invariant-workspace metadata without physical file materialization,
// and changes the working directory to the branch workspace.
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

	// 3. Create target workspace directory on disk
	targetRoot := opts.TargetDir
	if targetRoot == "" {
		targetRoot = opts.RepoName
	}
	branchDir := filepath.Join(targetRoot, branchName)
	if err := os.MkdirAll(branchDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create workspace directory %s: %w", branchDir, err)
	}

	// 4. Retrieve commit object to get base tree link
	commitObj, err := commitSvc.GetCommit(ctx, commitHash)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve commit object %s: %w", commitHash, err)
	}

	// 5. Create FUSE workspace layers via workspace package (no physical file materialization)
	wsLink, err := workspace.CreateWorkspace(
		ctx,
		store,
		slotsClient,
		nil,
		commitObj.Tree,
		opts.Layers,
	)
	if err != nil {
		return "", fmt.Errorf("failed to create FUSE workspace layers: %w", err)
	}

	// 6. Write .invariant-workspace metadata with FUSE content link
	meta := &WorkspaceMetadata{
		Content:      &wsLink,
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
