package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// BranchInfo holds metadata about a local or discovered branch.
type BranchInfo struct {
	Name         string `json:"name"`
	ShortName    string `json:"shortName"`
	CommitHash   string `json:"commitHash"`
	Upstream     string `json:"upstream,omitempty"`
	WorkspaceDir string `json:"workspaceDir,omitempty"`
	IsCurrent    bool   `json:"isCurrent"`
	IsLocal      bool   `json:"isLocal"`
	IsPeer       bool   `json:"isPeer"`
	Author       string `json:"author,omitempty"`
}

// ListBranches discovers all local branch workspaces, upstream branches, and peer branches.
func ListBranches(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	workspaceDir string,
) ([]BranchInfo, error) {
	_, currentMeta, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to locate workspace: %w", err)
	}

	repoName := currentMeta.RepoName
	repoRoot := filepath.Dir(currentMeta.WorkspaceDir)

	var branches []BranchInfo
	seenNames := make(map[string]bool)

	// 1. Discover local workspaces in repoRoot
	entries, err := os.ReadDir(repoRoot)
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			wsDir := filepath.Join(repoRoot, entry.Name())
			meta, err := ReadWorkspaceMetadata(wsDir)
			if err != nil {
				continue
			}

			// Read current commit from slot
			commitHash := meta.CommitHash
			if meta.SlotID != "" {
				if latestHash, err := slotsClient.Get(ctx, meta.SlotID); err == nil && latestHash != "" {
					commitHash = latestHash
				}
			}

			shortName := meta.BranchName
			if parts := strings.Split(meta.BranchName, ":"); len(parts) >= 4 {
				shortName = parts[3]
			}

			isCurrent := false
			if currentMeta != nil && currentMeta.WorkspaceDir == wsDir {
				isCurrent = true
			}

			bInfo := BranchInfo{
				Name:         meta.BranchName,
				ShortName:    shortName,
				CommitHash:   commitHash,
				Upstream:     meta.Upstream,
				WorkspaceDir: wsDir,
				IsCurrent:    isCurrent,
				IsLocal:      true,
				IsPeer:       false,
			}
			branches = append(branches, bInfo)
			seenNames[meta.BranchName] = true
			seenNames[shortName] = true
		}
	}

	// 2. Discover upstream branch in Names service if not already listed
	if upstreamEntry, err := namesClient.Get(ctx, repoName); err == nil && upstreamEntry.Value != "" {
		mainSlotID := upstreamEntry.Value
		if !seenNames["main"] {
			mainHash, _ := slotsClient.Get(ctx, mainSlotID)
			branches = append(branches, BranchInfo{
				Name:       "main",
				ShortName:  "main",
				CommitHash: mainHash,
				Upstream:   "",
				IsLocal:    false,
				IsPeer:     false,
			})
			seenNames["main"] = true
		}
	}

	// 3. Discover published peer branches via RepositoryConfig
	cfg, _, _, err := loadRepoConfigForTag(ctx, store, slotsClient, namesClient, repoName)
	if err == nil && cfg != nil && cfg.PeerBranches != nil {
		for branchFullName, slotID := range cfg.PeerBranches {
			if seenNames[branchFullName] {
				continue
			}

			parts := strings.Split(branchFullName, ":")
			author := ""
			shortBranch := branchFullName
			if len(parts) >= 4 {
				author = parts[1]
				shortBranch = parts[3]
			}
			commitHash, _ := slotsClient.Get(ctx, slotID)

			branches = append(branches, BranchInfo{
				Name:       branchFullName,
				ShortName:  shortBranch,
				CommitHash: commitHash,
				Upstream:   "main",
				IsLocal:    false,
				IsPeer:     true,
				Author:     author,
			})
			seenNames[branchFullName] = true
		}
	}

	// Sort branches: current branch first, then main, then local branches alphabetically, then peers
	sort.Slice(branches, func(i, j int) bool {
		if branches[i].IsCurrent != branches[j].IsCurrent {
			return branches[i].IsCurrent
		}
		if branches[i].ShortName == "main" && branches[j].ShortName != "main" {
			return true
		}
		if branches[j].ShortName == "main" && branches[i].ShortName != "main" {
			return false
		}
		if branches[i].IsLocal != branches[j].IsLocal {
			return branches[i].IsLocal
		}
		return branches[i].Name < branches[j].Name
	})

	return branches, nil
}

// FormatBranchList formats a list of branches for CLI display.
func FormatBranchList(branches []BranchInfo) string {
	if len(branches) == 0 {
		return "No branches found.\n"
	}

	var sb strings.Builder
	for _, b := range branches {
		marker := "  "
		if b.IsCurrent {
			marker = "* "
		}

		shortHash := b.CommitHash
		if len(shortHash) > 8 {
			shortHash = shortHash[:8]
		}
		if shortHash == "" {
			shortHash = "--------"
		}

		displayName := b.ShortName
		if b.IsPeer {
			displayName = b.Name
		}

		var tags []string
		if b.IsCurrent {
			tags = append(tags, "current")
		}
		if b.IsLocal {
			tags = append(tags, "local")
		}
		if b.IsPeer {
			tags = append(tags, fmt.Sprintf("peer: %s", b.Author))
		}
		if b.Upstream != "" && b.Upstream != b.ShortName {
			tags = append(tags, fmt.Sprintf("upstream: %s", b.Upstream))
		}

		tagStr := ""
		if len(tags) > 0 {
			tagStr = fmt.Sprintf(" (%s)", strings.Join(tags, ", "))
		}

		sb.WriteString(fmt.Sprintf("%s%-24s %s%s\n", marker, displayName, shortHash, tagStr))
	}

	return sb.String()
}

// DeleteBranch removes a branch workspace and unregisters it from the Names service.
func DeleteBranch(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	workspaceDir string,
	branchName string,
) error {
	if branchName == "" {
		return fmt.Errorf("branch name cannot be empty")
	}
	if branchName == "main" {
		return fmt.Errorf("cannot delete main branch")
	}

	_, currentMeta, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return fmt.Errorf("failed to locate workspace: %w", err)
	}

	repoRoot := filepath.Dir(currentMeta.WorkspaceDir)
	repoName := currentMeta.RepoName

	// 1. Check if there's a local workspace matching branchName
	var targetDir string
	var targetMeta *WorkspaceMetadata

	// Check by direct directory name
	directDir := filepath.Join(repoRoot, branchName)
	if meta, err := ReadWorkspaceMetadata(directDir); err == nil {
		targetDir = directDir
		targetMeta = meta
	} else {
		// Scan directories
		entries, _ := os.ReadDir(repoRoot)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			wsDir := filepath.Join(repoRoot, e.Name())
			meta, err := ReadWorkspaceMetadata(wsDir)
			if err == nil {
				if meta.BranchName == branchName || strings.HasSuffix(meta.BranchName, ":"+branchName) {
					targetDir = wsDir
					targetMeta = meta
					break
				}
			}
		}
	}

	// 2. If deleting the current workspace, switch directory out to repo root first
	if targetDir != "" {
		if currentMeta.WorkspaceDir == targetDir {
			_ = ChangeWorkingDirectory(repoRoot)
		}
		_ = os.RemoveAll(targetDir)
	}

	// 3. Unregister from Names Service if registered
	namesToDelete := []string{
		branchName,
		FormatChangeBranchName(CurrentIdentity(ctx).Name, repoName, branchName),
	}
	if targetMeta != nil {
		namesToDelete = append(namesToDelete, targetMeta.BranchName)
	}

	for _, name := range namesToDelete {
		if strings.HasPrefix(name, ":") {
			_ = namesClient.Delete(ctx, name, "")
		}
	}

	// 4. Also remove from RepositoryConfig.PeerBranches if present
	cfg, slotID, prevAddr, err := loadRepoConfigForTag(ctx, store, slotsClient, namesClient, repoName)
	if err == nil && cfg != nil && cfg.PeerBranches != nil {
		for _, name := range namesToDelete {
			delete(cfg.PeerBranches, name)
		}
		_ = saveRepoConfigForTag(ctx, store, slotsClient, namesClient, repoName, cfg, slotID, prevAddr)
	}

	return nil
}
