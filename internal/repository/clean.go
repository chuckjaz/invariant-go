package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// CleanOptions specifies parameters for cleaning the workspace overlay layer.
type CleanOptions struct {
	WorkspaceDir  string
	Force         bool
	RemoveDirs    bool
	RemoveIgnored bool
}

// CleanWorkspace removes untracked files and directories from the workspace.
func CleanWorkspace(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	commitSvc commit.Service,
	opts CleanOptions,
) ([]string, error) {
	_, meta, err := FindWorkspaceRoot(opts.WorkspaceDir)
	if err != nil {
		return nil, err
	}

	headCommitHash, err := slotsClient.Get(ctx, meta.SlotID)
	if err != nil {
		headCommitHash = meta.CommitHash
	}

	headCommit, err := commitSvc.GetCommit(ctx, headCommitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to read HEAD commit %s: %w", headCommitHash, err)
	}

	headEntries, err := commit.FlattenFileTree(ctx, headCommit.Tree.Address, store, slotsClient)
	if err != nil {
		return nil, fmt.Errorf("failed to flatten HEAD tree: %w", err)
	}

	var cleaned []string

	err = filepath.Walk(meta.WorkspaceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == meta.WorkspaceDir {
			return nil
		}

		rel, err := filepath.Rel(meta.WorkspaceDir, path)
		if err != nil {
			return err
		}
		if rel == ".invariant-workspace" || rel == ".git" || strings.HasPrefix(rel, ".ir-") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			if opts.RemoveDirs {
				// Check if any tracked files exist under this directory
				hasTracked := false
				for trackPath := range headEntries {
					if strings.HasPrefix(trackPath, rel+"/") {
						hasTracked = true
						break
					}
				}
				if !hasTracked {
					if opts.Force {
						os.RemoveAll(path)
					}
					cleaned = append(cleaned, rel+"/")
					return filepath.SkipDir
				}
			}
			return nil
		}

		// File check
		if _, tracked := headEntries[rel]; !tracked {
			if opts.Force {
				os.Remove(path)
			}
			cleaned = append(cleaned, rel)
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to clean workspace: %w", err)
	}

	return cleaned, nil
}
