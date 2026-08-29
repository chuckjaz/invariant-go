package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// ChangeOptions specifies parameters for creating a change branch workspace.
type ChangeOptions struct {
	RepoRoot       string
	ChangeName     string
	Private        bool
	UpstreamBranch string // default "main"
	AuthorName     string
}

// FindWorkspaceRoot walks up directory parents searching for .invariant-workspace.
func FindWorkspaceRoot(startDir string) (string, *WorkspaceMetadata, error) {
	curr, err := filepath.Abs(startDir)
	if err != nil {
		return "", nil, err
	}

	for {
		wsPath := filepath.Join(curr, ".invariant-workspace")
		data, err := os.ReadFile(wsPath)
		if err == nil {
			var meta WorkspaceMetadata
			if err := json.Unmarshal(data, &meta); err == nil {
				meta.WorkspaceDir = curr
				return curr, &meta, nil
			}
		}

		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}
	return "", nil, fmt.Errorf("not in an invariant repository workspace (no .invariant-workspace found)")
}

// CreateChangeBranch creates a writable change workspace branched from upstream.
func CreateChangeBranch(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	opts ChangeOptions,
) (*WorkspaceMetadata, error) {
	if opts.ChangeName == "" {
		return nil, fmt.Errorf("change branch name cannot be empty")
	}

	// 1. Resolve upstream branch metadata
	upstream := opts.UpstreamBranch
	if upstream == "" {
		upstream = "main"
	}

	repoName := filepath.Base(opts.RepoRoot)
	// Check if current directory has workspace metadata
	if _, meta, err := FindWorkspaceRoot(opts.RepoRoot); err == nil {
		repoName = meta.RepoName
	}

	// Lookup upstream slot
	upstreamEntry, err := namesClient.Get(ctx, repoName)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup repository %s in names service: %w", repoName, err)
	}
	upstreamSlotID := upstreamEntry.Value
	upstreamCommitHash, err := slotsClient.Get(ctx, upstreamSlotID)
	if err != nil {
		return nil, fmt.Errorf("failed to read upstream slot %s: %w", upstreamSlotID, err)
	}

	upstreamCommit, err := commitSvc.GetCommit(ctx, upstreamCommitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to read upstream commit %s: %w", upstreamCommitHash, err)
	}

	// 2. Allocate new change slot pointing to upstream HEAD commit
	changeSlotID, err := AllocateSlot(ctx, slotsClient, upstreamCommitHash, "")
	if err != nil {
		return nil, fmt.Errorf("failed to allocate change slot: %w", err)
	}

	// 3. Register change branch in Names Service unless private
	author := opts.AuthorName
	if author == "" {
		author = CurrentIdentity(ctx).Name
	}
	if !opts.Private {
		if err := RegisterChangeBranch(ctx, namesClient, author, repoName, opts.ChangeName, changeSlotID); err != nil {
			return nil, fmt.Errorf("failed to register change branch in names service: %w", err)
		}
		cfg, slotID, prevAddr, err := loadRepoConfigForTag(ctx, store, slotsClient, namesClient, repoName)
		if err == nil && cfg != nil {
			if cfg.PeerBranches == nil {
				cfg.PeerBranches = make(map[string]string)
			}
			cfg.PeerBranches[FormatChangeBranchName(author, repoName, opts.ChangeName)] = changeSlotID
			_ = saveRepoConfigForTag(ctx, store, slotsClient, namesClient, repoName, cfg, slotID, prevAddr)
		}
	}

	// 4. Create change branch directory and materialize workspace
	changeDir := filepath.Join(opts.RepoRoot, opts.ChangeName)
	if err := os.MkdirAll(changeDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create change directory %s: %w", changeDir, err)
	}

	if err := MaterializeTree(ctx, upstreamCommit.Tree, changeDir, store); err != nil {
		return nil, fmt.Errorf("failed to materialize tree in change directory %s: %w", changeDir, err)
	}

	// 5. Write workspace metadata
	branchName := FormatChangeBranchName(author, repoName, opts.ChangeName)
	if opts.Private {
		branchName = opts.ChangeName
	}

	meta := &WorkspaceMetadata{
		RepoName:       repoName,
		BranchName:     branchName,
		Upstream:       upstream,
		SlotID:         changeSlotID,
		CommitHash:     upstreamCommitHash,
		ParentSnapshot: upstreamCommitHash,
		Writable:       true,
		CreatedAt:      time.Now().Unix(),
		WorkspaceDir:   changeDir,
	}

	if err := WriteWorkspaceMetadata(changeDir, meta); err != nil {
		return nil, err
	}

	_ = ChangeWorkingDirectory(changeDir)

	return meta, nil
}
