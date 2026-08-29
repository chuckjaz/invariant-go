package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// CheckoutOptions specifies parameters for checking out a local or peer branch.
type CheckoutOptions struct {
	WorkspaceDir string
	BranchName   string
	Writable     bool
}

// CheckoutBranch checks out and switches to a local workspace, upstream branch, or peer branch.
func CheckoutBranch(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	opts CheckoutOptions,
) (*WorkspaceMetadata, error) {
	if opts.BranchName == "" {
		return nil, fmt.Errorf("branch name cannot be empty")
	}

	_, currentMeta, err := FindWorkspaceRoot(opts.WorkspaceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to locate current workspace: %w", err)
	}

	repoName := currentMeta.RepoName
	repoRoot := filepath.Dir(currentMeta.WorkspaceDir)

	// 1. Check if target branch is already a local workspace
	directDir := filepath.Join(repoRoot, opts.BranchName)
	if meta, err := ReadWorkspaceMetadata(directDir); err == nil {
		if err := ChangeWorkingDirectory(directDir); err != nil {
			return nil, err
		}
		return meta, nil
	}

	// Scan all local workspaces
	entries, _ := os.ReadDir(repoRoot)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wsDir := filepath.Join(repoRoot, e.Name())
		if meta, err := ReadWorkspaceMetadata(wsDir); err == nil {
			if meta.BranchName == opts.BranchName || strings.HasSuffix(meta.BranchName, ":"+opts.BranchName) {
				if err := ChangeWorkingDirectory(wsDir); err != nil {
					return nil, err
				}
				return meta, nil
			}
		}
	}

	// 2. Check if target branch is "main" or upstream
	if opts.BranchName == "main" || opts.BranchName == currentMeta.Upstream {
		mainDir := filepath.Join(repoRoot, "main")
		if _, err := os.Stat(mainDir); err == nil {
			if meta, err := ReadWorkspaceMetadata(mainDir); err == nil {
				if err := ChangeWorkingDirectory(mainDir); err != nil {
					return nil, err
				}
				return meta, nil
			}
		}

		// Main workspace directory doesn't exist locally; mount it
		meta, err := MountRepository(ctx, store, slotsClient, namesClient, commitSvc, repoName, repoRoot)
		if err != nil {
			return nil, fmt.Errorf("failed to mount main branch: %w", err)
		}
		return meta, nil
	}

	// 3. Resolve peer change branch in Names Service
	var targetEntry *names.NameEntry
	var fullBranchName string
	var peerAuthor string
	var shortBranchName string

	if strings.HasPrefix(opts.BranchName, ":") {
		// Exact registered name: :<user>:<repo>:<branch>
		fullBranchName = opts.BranchName
		if entry, err := namesClient.Get(ctx, fullBranchName); err == nil && entry.Value != "" {
			targetEntry = &entry
		}
	} else if strings.Contains(opts.BranchName, ":") {
		// Formatted as user:branch
		parts := strings.Split(opts.BranchName, ":")
		fullBranchName = fmt.Sprintf(":%s:%s:%s", parts[0], repoName, parts[1])
		if entry, err := namesClient.Get(ctx, fullBranchName); err == nil && entry.Value != "" {
			targetEntry = &entry
		}
	} else {
		// Try direct name in Names service
		if entry, err := namesClient.Get(ctx, opts.BranchName); err == nil && entry.Value != "" {
			targetEntry = &entry
			fullBranchName = opts.BranchName
		}
	}

	// Also check RepositoryConfig.PeerBranches
	if targetEntry == nil {
		cfg, _, _, err := loadRepoConfigForTag(ctx, store, slotsClient, namesClient, repoName)
		if err == nil && cfg != nil && cfg.PeerBranches != nil {
			for bName, slotID := range cfg.PeerBranches {
				if bName == opts.BranchName || strings.HasSuffix(bName, ":"+opts.BranchName) {
					entry := names.NameEntry{Value: slotID}
					targetEntry = &entry
					fullBranchName = bName
					break
				}
			}
		}
	}

	if targetEntry == nil {
		return nil, fmt.Errorf("branch %q not found locally or in names service", opts.BranchName)
	}

	parts := strings.Split(fullBranchName, ":")
	if len(parts) >= 4 {
		peerAuthor = parts[1]
		shortBranchName = parts[3]
	} else {
		shortBranchName = opts.BranchName
	}

	// 4. Read HEAD commit from peer slot
	peerSlotID := targetEntry.Value
	headCommitHash, err := slotsClient.Get(ctx, peerSlotID)
	if err != nil || headCommitHash == "" {
		return nil, fmt.Errorf("failed to read commit from peer slot %s: %w", peerSlotID, err)
	}

	commitObj, err := commitSvc.GetCommit(ctx, headCommitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to read commit %s: %w", headCommitHash, err)
	}

	// 5. Determine local workspace directory
	localDirName := shortBranchName
	localWsDir := filepath.Join(repoRoot, localDirName)
	if _, err := os.Stat(localWsDir); err == nil {
		// Already occupied by a different branch directory, use peer prefix
		localDirName = fmt.Sprintf("%s-%s", peerAuthor, shortBranchName)
		localWsDir = filepath.Join(repoRoot, localDirName)
	}

	if err := os.MkdirAll(localWsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create checkout directory %s: %w", localWsDir, err)
	}

	// 6. Materialize files from peer commit
	if err := MaterializeTree(ctx, commitObj.Tree, localWsDir, store); err != nil {
		return nil, fmt.Errorf("failed to materialize tree in %s: %w", localWsDir, err)
	}

	// 7. Write workspace metadata
	meta := &WorkspaceMetadata{
		RepoName:       repoName,
		BranchName:     fullBranchName,
		Upstream:       "main",
		SlotID:         peerSlotID,
		CommitHash:     headCommitHash,
		ParentSnapshot: headCommitHash,
		Writable:       opts.Writable,
		CreatedAt:      time.Now().Unix(),
		WorkspaceDir:   localWsDir,
	}

	if err := WriteWorkspaceMetadata(localWsDir, meta); err != nil {
		return nil, err
	}

	// 8. Switch directory
	if err := ChangeWorkingDirectory(localWsDir); err != nil {
		return nil, err
	}

	return meta, nil
}
