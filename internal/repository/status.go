package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// FileStatus represents the change kind of a file in the workspace.
type FileStatus string

const (
	StatusAdded    FileStatus = "added"
	StatusModified FileStatus = "modified"
	StatusDeleted  FileStatus = "deleted"
)

// StatusEntry records the status of an individual file.
type StatusEntry struct {
	Path   string     `json:"path"`
	Status FileStatus `json:"status"`
}

// StatusResult aggregates workspace status entries.
type StatusResult struct {
	RepoName   string        `json:"repoName"`
	BranchName string        `json:"branchName"`
	HeadCommit string        `json:"headCommit"`
	Entries    []StatusEntry `json:"entries"`
}

// GetStatus computes the status of files in a workspace relative to its HEAD commit.
func GetStatus(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	commitSvc commit.Service,
	workspaceDir string,
) (*StatusResult, error) {
	_, meta, err := FindWorkspaceRoot(workspaceDir)
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

	// Read working directory files on disk
	diskFiles := make(map[string][]byte)
	err = filepath.Walk(meta.WorkspaceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == ".invariant-workspace" || strings.HasPrefix(info.Name(), ".ir-") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(meta.WorkspaceDir, path)
		if err != nil {
			return err
		}
		if rel == ".invariant-workspace" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		diskFiles[rel] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk workspace directory: %w", err)
	}

	var entries []StatusEntry

	// Check disk files vs HEAD
	for path, diskData := range diskFiles {
		headEntry, inHead := headEntries[path]
		if !inHead {
			entries = append(entries, StatusEntry{Path: path, Status: StatusAdded})
		} else {
			// Compare content
			headLines, err := commit.ReadFileLines(ctx, headEntry.Address, store, slotsClient)
			if err == nil {
				headContent := strings.Join(headLines, "\n")
				if len(headLines) > 0 {
					headContent += "\n"
				}
				if string(diskData) != headContent && string(diskData) != strings.Join(headLines, "\n") {
					entries = append(entries, StatusEntry{Path: path, Status: StatusModified})
				}
			} else {
				entries = append(entries, StatusEntry{Path: path, Status: StatusModified})
			}
		}
	}

	// Check deleted files in HEAD but not on disk
	for path := range headEntries {
		if _, onDisk := diskFiles[path]; !onDisk {
			entries = append(entries, StatusEntry{Path: path, Status: StatusDeleted})
		}
	}

	// Sort entries by path
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})

	return &StatusResult{
		RepoName:   meta.RepoName,
		BranchName: meta.BranchName,
		HeadCommit: headCommitHash,
		Entries:    entries,
	}, nil
}
