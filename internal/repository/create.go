package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"invariant/internal/content"
	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// CreateOptions specifies parameters for creating a new repository.
type CreateOptions struct {
	Name       string
	Branch     string // Initial branch name (default: "main")
	Directory  string // Initial content path on disk
	Content    string // Initial CAS address / content link
	CreateOnly bool
	Encrypted  bool
	Compressed bool
	Writable   bool
	TargetDir  string // Target directory where repo root will be mounted (default: ./<name>)
}

// WorkspaceMetadata represents the persistent metadata stored in a repository workspace directory.
type WorkspaceMetadata struct {
	RepoName       string `json:"repoName"`
	BranchName     string `json:"branchName"`
	Upstream       string `json:"upstream"`
	SlotID         string `json:"slotId"`
	CommitHash     string `json:"commitHash"`
	ParentSnapshot string `json:"parentSnapshot,omitempty"`
	Writable       bool   `json:"writable"`
	CreatedAt      int64  `json:"createdAt"`
	WorkspaceDir   string `json:"workspaceDir,omitempty"`
}

// CreateRepository creates a new repository, initializes root commit and main branch,
// and mounts the workspace unless CreateOnly is set.
func CreateRepository(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	opts CreateOptions,
) (*RepositoryConfig, string, error) {
	if opts.Name == "" {
		return nil, "", fmt.Errorf("repository name cannot be empty")
	}

	branchName := opts.Branch
	if branchName == "" {
		branchName = "main"
	}

	// 1. Determine initial content
	var initialTree content.ContentLink
	var rootCommitHash string

	if opts.Directory != "" {
		treeLink, err := SnapshotDirectory(ctx, opts.Directory, store)
		if err != nil {
			return nil, "", fmt.Errorf("failed to snapshot directory %s: %w", opts.Directory, err)
		}
		initialTree = treeLink
	} else if opts.Content != "" {
		var link content.ContentLink
		trimmedContent := strings.TrimSpace(opts.Content)
		if strings.HasPrefix(trimmedContent, "{") {
			if err := json.Unmarshal([]byte(trimmedContent), &link); err != nil {
				return nil, "", fmt.Errorf("failed to parse content link JSON: %w", err)
			}
		} else {
			link = content.ContentLink{Address: trimmedContent}
		}

		// Check if the provided link points directly to an existing Commit object in CAS
		if c, err := commitSvc.GetCommit(ctx, link.Address); err == nil && c != nil && c.Tree.Address != "" {
			rootCommitHash = link.Address
			initialTree = c.Tree
		} else {
			initialTree = link
		}
	} else {
		treeLink, err := CreateEmptyTree(ctx, store)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create initial empty tree: %w", err)
		}
		initialTree = treeLink
	}

	// 2. Create root initial commit if not already using an existing commit
	if rootCommitHash == "" {
		_, newHash, err := commitSvc.CreateCommit(ctx, commit.CreateRequest{
			RepoName:   opts.Name,
			BranchName: branchName,
			TreeLink:   initialTree,
			Message:    "Initial commit",
		})
		if err != nil {
			return nil, "", fmt.Errorf("failed to create root commit: %w", err)
		}
		rootCommitHash = newHash
	}

	// 3. Allocate main branch slot
	mainSlotID, err := AllocateSlot(ctx, slotsClient, rootCommitHash, "")
	if err != nil {
		return nil, "", fmt.Errorf("failed to allocate main branch slot: %w", err)
	}

	// 4. Create and store RepositoryConfig
	cfg := &RepositoryConfig{
		DefaultBranch:  branchName,
		MainSlotID:     mainSlotID,
		Encrypted:      opts.Encrypted,
		Compressed:     opts.Compressed,
		ReviewRequired: false,
		Tags:           make(map[string]string),
		PeerBranches:   make(map[string]string),
		Settings:       make(map[string]string),
		CreatedAt:      time.Now().Unix(),
	}
	cfgAddr, err := WriteRepositoryConfig(ctx, store, cfg)
	if err == nil {
		cfgSlotID, err := AllocateSlot(ctx, slotsClient, cfgAddr, "")
		if err == nil {
			_ = namesClient.Put(ctx, opts.Name+":config", cfgSlotID, nil)
		}
	}

	// 5. Register repository name in Names Service
	if err := RegisterRepositoryName(ctx, namesClient, opts.Name, mainSlotID); err != nil {
		return nil, "", fmt.Errorf("failed to register repository %s in names service: %w", opts.Name, err)
	}
	if branchName != "main" {
		_ = namesClient.Put(ctx, opts.Name+":"+branchName, mainSlotID, nil)
	}

	// 6. Set up local workspace if not CreateOnly
	if !opts.CreateOnly {
		targetRoot := opts.TargetDir
		if targetRoot == "" {
			targetRoot = opts.Name
		}
		branchDir := filepath.Join(targetRoot, branchName)
		if err := os.MkdirAll(branchDir, 0755); err != nil {
			return nil, "", fmt.Errorf("failed to create main workspace directory %s: %w", branchDir, err)
		}

		// Materialize initial files
		if err := MaterializeTree(ctx, initialTree, branchDir, store); err != nil {
			return nil, "", fmt.Errorf("failed to materialize initial tree in %s: %w", branchDir, err)
		}

		// Write workspace metadata
		meta := &WorkspaceMetadata{
			RepoName:     opts.Name,
			BranchName:   branchName,
			Upstream:     branchName,
			SlotID:       mainSlotID,
			CommitHash:   rootCommitHash,
			Writable:     opts.Writable,
			CreatedAt:    time.Now().Unix(),
			WorkspaceDir: branchDir,
		}
		if err := WriteWorkspaceMetadata(branchDir, meta); err != nil {
			return nil, "", err
		}
		_ = ChangeWorkingDirectory(branchDir)
	}

	return cfg, rootCommitHash, nil
}

// ReadWorkspaceMetadata loads .invariant-workspace from wsRoot.
func ReadWorkspaceMetadata(wsRoot string) (*WorkspaceMetadata, error) {
	wsPath := filepath.Join(wsRoot, ".invariant-workspace")
	data, err := os.ReadFile(wsPath)
	if err != nil {
		return nil, err
	}
	var meta WorkspaceMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	meta.WorkspaceDir = wsRoot
	return &meta, nil
}

// WriteWorkspaceMetadata saves .invariant-workspace to wsRoot.
func WriteWorkspaceMetadata(wsRoot string, meta *WorkspaceMetadata) error {
	wsPath := filepath.Join(wsRoot, ".invariant-workspace")
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(wsPath, data, 0644)
}
