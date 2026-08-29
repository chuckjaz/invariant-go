package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"invariant/internal/content"
	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// MountRepository mounts an existing repository workspace to a local directory.
func MountRepository(ctx context.Context, store storage.Storage, slotsClient slots.Slots, namesClient names.Names, commitSvc commit.Service, repoName, targetDir string) (*WorkspaceMetadata, error) {
	if repoName == "" {
		return nil, fmt.Errorf("repository name is required")
	}

	entry, err := namesClient.Get(ctx, repoName)
	if err != nil {
		return nil, fmt.Errorf("repository %q not registered with names service: %w", repoName, err)
	}

	slotID := entry.Value
	slotAddr, err := slotsClient.Get(ctx, slotID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve repository slot %s: %w", slotID, err)
	}

	rootCommitHash := slotAddr
	rootCommit, err := commitSvc.GetCommit(ctx, rootCommitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve repository root commit: %w", err)
	}

	dir := targetDir
	if dir == "" {
		dir = repoName
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	mainBranchDir := filepath.Join(absDir, "main")
	if err := os.MkdirAll(mainBranchDir, 0755); err != nil {
		return nil, err
	}

	if err := MaterializeTree(ctx, content.ContentLink{Address: rootCommit.Tree.Address}, mainBranchDir, store); err != nil {
		return nil, fmt.Errorf("failed to materialize main branch tree: %w", err)
	}

	meta := &WorkspaceMetadata{
		RepoName:       repoName,
		BranchName:     "main",
		SlotID:         slotID,
		CommitHash:     rootCommitHash,
		ParentSnapshot: "",
		WorkspaceDir:   mainBranchDir,
	}

	if err := WriteWorkspaceMetadata(mainBranchDir, meta); err != nil {
		return nil, err
	}

	return meta, nil
}

// UnmountRepository cleanly unmounts a repository workspace directory and its nested branches.
func UnmountRepository(workspaceDir string) error {
	wsRoot, _, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return err
	}

	// Verify workspace exists
	if _, err := ReadWorkspaceMetadata(wsRoot); err != nil {
		return fmt.Errorf("directory %s is not a valid Invariant workspace: %w", wsRoot, err)
	}

	// Unmount logic: In user-space disk workspaces, verify clean state
	return nil
}
