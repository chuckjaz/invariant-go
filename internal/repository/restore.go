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

// RestoreOptions specifies parameters for restoring files from a commit tree.
type RestoreOptions struct {
	WorkspaceDir string
	Path         string
	SourceCommit string
}

// RestoreFiles discards uncommitted changes and restores target files or the entire workspace from CAS.
func RestoreFiles(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitSvc commit.Service, opts RestoreOptions) ([]string, error) {
	wsRoot, meta, err := FindWorkspaceRoot(opts.WorkspaceDir)
	if err != nil {
		return nil, err
	}

	sourceCommitHash := opts.SourceCommit
	if sourceCommitHash == "" {
		if meta.SlotID != "" {
			slotAddr, err := slotsClient.Get(ctx, meta.SlotID)
			if err == nil && slotAddr != "" {
				sourceCommitHash = slotAddr
			}
		}
		if sourceCommitHash == "" {
			sourceCommitHash = meta.CommitHash
		}
	}

	c, err := commitSvc.GetCommit(ctx, sourceCommitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve source commit %q: %w", sourceCommitHash, err)
	}

	treeEntries, err := commit.FlattenFileTree(ctx, c.Tree.Address, store, slotsClient)
	if err != nil {
		return nil, fmt.Errorf("failed to traverse source commit tree: %w", err)
	}

	var restored []string

	targetPath := strings.TrimSpace(opts.Path)
	if targetPath != "" {
		relPath, err := FindWorkspaceRelativePath(wsRoot, targetPath)
		if err != nil {
			relPath = filepathClean(targetPath)
		}

		entry, existsInTree := treeEntries[relPath]
		diskPath := filepath.Join(wsRoot, relPath)

		if existsInTree && entry.Address != "" {
			data, err := readTreeObjectContent(ctx, store, entry.Address)
			if err != nil {
				return nil, fmt.Errorf("failed to read object data for %s: %w", relPath, err)
			}

			if err := os.MkdirAll(filepath.Dir(diskPath), 0755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(diskPath, data, 0644); err != nil {
				return nil, err
			}
			restored = append(restored, relPath)
		} else {
			// File does not exist in HEAD tree; remove if present on disk
			if _, err := os.Stat(diskPath); err == nil {
				_ = os.Remove(diskPath)
				restored = append(restored, relPath)
			}
		}

		return restored, nil
	}

	// Restore entire workspace
	for relPath, entry := range treeEntries {
		if entry.Address == "" {
			continue
		}
		diskPath := filepath.Join(wsRoot, relPath)
		data, err := readTreeObjectContent(ctx, store, entry.Address)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", relPath, err)
		}

		if err := os.MkdirAll(filepath.Dir(diskPath), 0755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(diskPath, data, 0644); err != nil {
			return nil, err
		}
		restored = append(restored, relPath)
	}

	return restored, nil
}
