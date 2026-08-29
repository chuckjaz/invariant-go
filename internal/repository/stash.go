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
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
	"invariant/internal/workspace"
)

// StashEntry records metadata for a shelved commit snapshot.
type StashEntry struct {
	CommitHash string `json:"commitHash"`
	ParentHash string `json:"parentHash"`
	BranchName string `json:"branchName"`
	Message    string `json:"message"`
	Timestamp  int64  `json:"timestamp"`
}

const stashFileName = ".invariant-stash.json"

func readStashStack(wsRoot string) ([]StashEntry, error) {
	stashPath := filepath.Join(wsRoot, stashFileName)
	data, err := os.ReadFile(stashPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var stack []StashEntry
	if err := json.Unmarshal(data, &stack); err != nil {
		return nil, err
	}
	return stack, nil
}

func writeStashStack(wsRoot string, stack []StashEntry) error {
	stashPath := filepath.Join(wsRoot, stashFileName)
	if len(stack) == 0 {
		_ = os.Remove(stashPath)
		return nil
	}
	data, err := json.MarshalIndent(stack, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(stashPath, data, 0644)
}

// StashPush snapshots uncommitted changes to a temporary CAS commit and restores working tree to HEAD.
func StashPush(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitSvc commit.Service, workspaceDir, msg string) (string, error) {
	wsRoot, meta, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return "", err
	}

	currentHeadHash := meta.CommitHash
	if meta.SlotID != "" {
		slotAddr, err := slotsClient.Get(ctx, meta.SlotID)
		if err == nil && slotAddr != "" {
			currentHeadHash = slotAddr
		}
	}

	// Check if working tree has changes
	statusRes, err := GetStatus(ctx, store, slotsClient, commitSvc, wsRoot)
	if err != nil {
		return "", err
	}
	if len(statusRes.Entries) == 0 {
		return "", fmt.Errorf("no local changes to save")
	}

	// Snapshot working tree to CAS
	snapshotLink, err := SnapshotDirectory(ctx, wsRoot, store)
	if err != nil {
		return "", fmt.Errorf("failed to snapshot working tree: %w", err)
	}

	headCommit, err := commitSvc.GetCommit(ctx, currentHeadHash)
	if err != nil {
		return "", err
	}

	stashMsg := msg
	if stashMsg == "" {
		stashMsg = fmt.Sprintf("WIP on %s: %s", meta.BranchName, strings.Split(headCommit.Message, "\n")[0])
	}

	now := time.Now().Unix()
	stashCommit, stashHash, err := commitSvc.CreateCommit(ctx, commit.CreateRequest{
		RepoName:   meta.RepoName,
		BranchName: meta.BranchName,
		TreeLink:   snapshotLink,
		Parents:    []string{currentHeadHash},
		Message:    stashMsg,
		Author:     headCommit.Author,
		Tags: map[string]string{
			"stash": fmt.Sprintf("%d", now),
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create stash commit: %w", err)
	}
	_ = stashCommit

	// Add to stash stack
	stack, _ := readStashStack(wsRoot)
	entry := StashEntry{
		CommitHash: stashHash,
		ParentHash: currentHeadHash,
		BranchName: meta.BranchName,
		Message:    stashMsg,
		Timestamp:  now,
	}
	stack = append([]StashEntry{entry}, stack...)
	if err := writeStashStack(wsRoot, stack); err != nil {
		return "", fmt.Errorf("failed to save stash stack: %w", err)
	}

	// Restore working tree to clean HEAD
	_, err = RestoreFiles(ctx, store, slotsClient, commitSvc, RestoreOptions{
		WorkspaceDir: wsRoot,
		SourceCommit: currentHeadHash,
	})
	if err != nil {
		return "", fmt.Errorf("failed to restore clean working tree: %w", err)
	}

	// Clean any newly created files
	_, _ = CleanWorkspace(ctx, store, slotsClient, commitSvc, CleanOptions{
		WorkspaceDir: wsRoot,
		Force:        true,
		RemoveDirs:   true,
	})

	return stashHash, nil
}

// StashPop applies the latest stash snapshot onto the working tree and removes it from the stash stack.
func StashPop(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitSvc commit.Service, workspaceDir string) (string, error) {
	wsRoot, meta, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return "", err
	}

	stack, err := readStashStack(wsRoot)
	if err != nil || len(stack) == 0 {
		return "", fmt.Errorf("no stash entries found")
	}

	top := stack[0]
	stashCommit, err := commitSvc.GetCommit(ctx, top.CommitHash)
	if err != nil {
		return "", fmt.Errorf("stash commit %s not found: %w", top.CommitHash, err)
	}

	currentHeadHash := meta.CommitHash
	if meta.SlotID != "" {
		slotAddr, err := slotsClient.Get(ctx, meta.SlotID)
		if err == nil && slotAddr != "" {
			currentHeadHash = slotAddr
		}
	}

	headCommit, err := commitSvc.GetCommit(ctx, currentHeadHash)
	if err != nil {
		return "", err
	}

	// 3-way merge:
	// Ancestor = top.ParentHash (base commit when stashed)
	// Ours     = headCommit.Tree (current HEAD)
	// Theirs   = stashCommit.Tree (stashed edits)
	ancestorTree := headCommit.Tree.Address
	if top.ParentHash != "" {
		pCommit, err := commitSvc.GetCommit(ctx, top.ParentHash)
		if err == nil && pCommit != nil {
			ancestorTree = pCommit.Tree.Address
		}
	}

	mergedTreeAddr, conflicts, err := workspace.MergeTrees(
		ctx,
		ancestorTree,
		headCommit.Tree.Address,
		stashCommit.Tree.Address,
		store,
		slotsClient,
	)
	if err != nil {
		return "", fmt.Errorf("failed to merge stash: %w", err)
	}

	if len(conflicts) > 0 {
		return "", fmt.Errorf("stash pop resulted in %d merge conflict(s)", len(conflicts))
	}

	// Materialize merged tree to disk
	if err := MaterializeTree(ctx, content.ContentLink{Address: mergedTreeAddr}, wsRoot, store); err != nil {
		return "", fmt.Errorf("failed to materialize stashed files: %w", err)
	}

	// Pop top from stack
	stack = stack[1:]
	_ = writeStashStack(wsRoot, stack)

	return top.Message, nil
}

// StashList returns all stashed snapshots in the workspace.
func StashList(workspaceDir string) ([]StashEntry, error) {
	wsRoot, _, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return nil, err
	}
	return readStashStack(wsRoot)
}

// StashDrop removes a specific stash entry by index (0-indexed).
func StashDrop(workspaceDir string, index int) error {
	wsRoot, _, err := FindWorkspaceRoot(workspaceDir)
	if err != nil {
		return err
	}

	stack, err := readStashStack(wsRoot)
	if err != nil || len(stack) == 0 {
		return fmt.Errorf("no stash entries found")
	}

	if index < 0 || index >= len(stack) {
		return fmt.Errorf("stash index %d out of range (total %d)", index, len(stack))
	}

	stack = append(stack[:index], stack[index+1:]...)
	return writeStashStack(wsRoot, stack)
}

// FormatStashList formats the stash stack for CLI display.
func FormatStashList(entries []StashEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, e := range entries {
		sb.WriteString(fmt.Sprintf("stash@{%d}: %s\n", i, e.Message))
	}
	return sb.String()
}
