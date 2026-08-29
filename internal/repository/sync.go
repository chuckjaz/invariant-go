package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// SyncOptions specifies parameters for synchronizing and rebasing a change workspace.
type SyncOptions struct {
	WorkspaceDir string
	Continue     bool
	Abort        bool
}

// SyncState captures in-flight rebase/sync conflict information.
type SyncState struct {
	PreSyncCommit string   `json:"preSyncCommit"`
	TargetCommit  string   `json:"targetCommit"`
	Conflicts     []string `json:"conflicts"`
}

// ExecuteSync performs a 3-way rebase of the workspace with conflict resolution controls.
func ExecuteSync(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	opts SyncOptions,
) (string, []string, error) {
	_, meta, err := FindWorkspaceRoot(opts.WorkspaceDir)
	if err != nil {
		return "", nil, err
	}

	statePath := filepath.Join(meta.WorkspaceDir, ".ir-sync-state.json")

	// Case 1: Abort sync
	if opts.Abort {
		stateData, err := os.ReadFile(statePath)
		if err != nil {
			return "", nil, fmt.Errorf("no sync in progress to abort")
		}
		var state SyncState
		if err := json.Unmarshal(stateData, &state); err != nil {
			return "", nil, fmt.Errorf("corrupted sync state: %w", err)
		}

		preCommit, err := commitSvc.GetCommit(ctx, state.PreSyncCommit)
		if err != nil {
			return "", nil, fmt.Errorf("failed to read pre-sync commit %s: %w", state.PreSyncCommit, err)
		}

		// Restore workspace files
		if err := MaterializeTree(ctx, preCommit.Tree, meta.WorkspaceDir, store); err != nil {
			return "", nil, fmt.Errorf("failed to restore pre-sync tree: %w", err)
		}

		_ = os.Remove(statePath)
		return state.PreSyncCommit, nil, nil
	}

	// Case 2: Continue sync after resolving conflicts
	if opts.Continue {
		stateData, err := os.ReadFile(statePath)
		if err != nil {
			return "", nil, fmt.Errorf("no sync in progress to continue")
		}
		var state SyncState
		_ = json.Unmarshal(stateData, &state)

		// Verify no unresolved conflict markers remain in any workspace file
		var remainingConflicts []string
		_ = filepath.Walk(meta.WorkspaceDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || info.Name() == ".invariant-workspace" || strings.HasPrefix(info.Name(), ".ir-") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err == nil {
				contentStr := string(data)
				if strings.Contains(contentStr, "<<<<<<<") && strings.Contains(contentStr, "=======") && strings.Contains(contentStr, ">>>>>>>") {
					rel, _ := filepath.Rel(meta.WorkspaceDir, path)
					remainingConflicts = append(remainingConflicts, rel)
				}
			}
			return nil
		})

		if len(remainingConflicts) > 0 {
			return "", remainingConflicts, fmt.Errorf("unresolved conflict markers remain in: %s", strings.Join(remainingConflicts, ", "))
		}

		// Snapshot resolved tree and create commit
		treeLink, err := SnapshotDirectory(ctx, meta.WorkspaceDir, store)
		if err != nil {
			return "", nil, fmt.Errorf("failed to snapshot resolved workspace: %w", err)
		}

		c, hash, err := commitSvc.CreateCommit(ctx, commit.CreateRequest{
			RepoName:   meta.RepoName,
			BranchName: meta.BranchName,
			TreeLink:   treeLink,
			Parents:    []string{state.TargetCommit},
			Message:    "Rebased change after conflict resolution",
		})
		if err != nil {
			return "", nil, fmt.Errorf("failed to create rebased commit: %w", err)
		}
		_ = c

		_ = os.Remove(statePath)
		meta.CommitHash = hash
		metaData, _ := json.MarshalIndent(meta, "", "  ")
		_ = os.WriteFile(filepath.Join(meta.WorkspaceDir, ".invariant-workspace"), metaData, 0644)

		return hash, nil, nil
	}

	// Case 3: Initial sync execution
	preSyncHash, _ := slotsClient.Get(ctx, meta.SlotID)
	if preSyncHash == "" {
		preSyncHash = meta.CommitHash
	}

	newHead, conflicts, err := commitSvc.SyncBranch(ctx, meta.RepoName, meta.BranchName)
	if err != nil {
		return "", nil, err
	}

	if len(conflicts) > 0 {
		// Record conflict state
		state := SyncState{
			PreSyncCommit: preSyncHash,
			Conflicts:     conflicts,
		}
		stateData, _ := json.MarshalIndent(state, "", "  ")
		_ = os.WriteFile(statePath, stateData, 0644)

		// Add conflict markers to conflicting files
		for _, confFile := range conflicts {
			p := filepath.Join(meta.WorkspaceDir, confFile)
			existing, _ := os.ReadFile(p)
			var sb strings.Builder
			sb.WriteString("<<<<<<< HEAD (Current Branch)\n")
			sb.Write(existing)
			sb.WriteString("\n=======\n")
			sb.WriteString(">>>>>>> Incoming Upstream Change\n")
			_ = os.WriteFile(p, []byte(sb.String()), 0644)
		}

		return "", conflicts, nil
	}

	// Clean merge: update local workspace files to rebased commit
	rebasedCommit, err := commitSvc.GetCommit(ctx, newHead)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read rebased commit %s: %w", newHead, err)
	}

	if err := MaterializeTree(ctx, rebasedCommit.Tree, meta.WorkspaceDir, store); err != nil {
		return "", nil, fmt.Errorf("failed to materialize rebased tree: %w", err)
	}

	meta.CommitHash = newHead
	metaData, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(meta.WorkspaceDir, ".invariant-workspace"), metaData, 0644)

	return newHead, nil, nil
}
