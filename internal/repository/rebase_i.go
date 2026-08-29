package repository

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"invariant/internal/content"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// GenerateRebasePlanText creates the editable action sheet for interactive rebase.
func GenerateRebasePlanText(commits []*commit.Commit, hashes []string, upstreamName string) string {
	var sb strings.Builder
	sb.WriteString("# Interactive Rebase Plan\n")
	sb.WriteString(fmt.Sprintf("# Commands: pick, reword, edit, squash, drop\n# Base: %s\n#\n", upstreamName))

	// List in chronological order (oldest to newest)
	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		hash := hashes[i]
		shortHash := hash
		if len(shortHash) > 8 {
			shortHash = shortHash[:8]
		}
		firstLine := strings.Split(strings.TrimSpace(c.Message), "\n")[0]
		sb.WriteString(fmt.Sprintf("pick %s %s\n", shortHash, firstLine))
	}

	sb.WriteString("\n# Reorder lines to change commit order.\n# Delete a line or use 'drop' to discard a commit.\n# Change 'pick' to 'squash' to combine with the previous commit.\n# Change 'pick' to 'reword' to edit the commit message.\n")
	return sb.String()
}

// ParseRebasePlan parses the edited interactive rebase plan text.
func ParseRebasePlan(planText string, availableCommits map[string]string) ([]commit.RebaseAction, error) {
	var actions []commit.RebaseAction
	scanner := bufio.NewScanner(strings.NewReader(planText))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		actionVerb := strings.ToLower(fields[0])
		shortHash := fields[1]

		fullHash := shortHash
		if availableCommits != nil {
			for prefix, full := range availableCommits {
				if strings.HasPrefix(full, shortHash) || prefix == shortHash {
					fullHash = full
					break
				}
			}
		}

		var actionType commit.RebaseActionType
		switch actionVerb {
		case "pick", "p":
			actionType = commit.RebasePick
		case "squash", "s":
			actionType = commit.RebaseSquash
		case "edit", "e":
			actionType = commit.RebaseEdit
		case "reword", "r":
			actionType = commit.RebaseReword
		case "drop", "d":
			actionType = commit.RebaseDrop
		default:
			return nil, fmt.Errorf("unknown rebase action verb: %q", actionVerb)
		}

		msg := ""
		if len(fields) > 2 {
			msg = strings.Join(fields[2:], " ")
		}

		actions = append(actions, commit.RebaseAction{
			Type:       actionType,
			CommitHash: fullHash,
			NewMessage: msg,
		})
	}

	return actions, nil
}

// ExecuteInteractiveRebase runs an interactive rebase session for the current change branch.
func ExecuteInteractiveRebase(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitSvc commit.Service, workspaceDir, upstreamBranch string, customPlan string) (string, error) {
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

	// Determine base commit (upstream branch)
	baseCommit := meta.ParentSnapshot
	if baseCommit == "" {
		baseCommit = meta.CommitHash
	}

	commits, hashes, err := commitSvc.GetHistory(ctx, currentHeadHash, true, "")
	if err != nil {
		return "", fmt.Errorf("failed to fetch branch history: %w", err)
	}

	// Filter commits that belong to this change branch (after baseCommit)
	var changeCommits []*commit.Commit
	var changeHashes []string
	commitMap := make(map[string]string)

	for i, c := range commits {
		h := hashes[i]
		if h == baseCommit {
			break
		}
		changeCommits = append(changeCommits, c)
		changeHashes = append(changeHashes, h)
		commitMap[h[:min(8, len(h))]] = h
		commitMap[h] = h
	}

	if len(changeCommits) == 0 {
		return currentHeadHash, fmt.Errorf("no commits to rebase on top of %s", baseCommit)
	}

	var plan []commit.RebaseAction
	if customPlan != "" {
		plan, err = ParseRebasePlan(customPlan, commitMap)
		if err != nil {
			return "", err
		}
	} else {
		planText := GenerateRebasePlanText(changeCommits, changeHashes, baseCommit)
		tmpFile := filepath.Join(wsRoot, ".ir-rebase-todo")
		if err := os.WriteFile(tmpFile, []byte(planText), 0600); err != nil {
			return "", err
		}
		defer os.Remove(tmpFile)

		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "nano"
		}
		cmd := exec.Command(editor, tmpFile)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("editor failed: %w", err)
		}

		editedData, err := os.ReadFile(tmpFile)
		if err != nil {
			return "", err
		}
		plan, err = ParseRebasePlan(string(editedData), commitMap)
		if err != nil {
			return "", err
		}
	}

	if len(plan) == 0 {
		return currentHeadHash, fmt.Errorf("rebase plan is empty; aborting")
	}

	newHead, err := commitSvc.InteractiveRebase(ctx, meta.RepoName, meta.BranchName, baseCommit, plan)
	if err != nil {
		return "", fmt.Errorf("interactive rebase execution failed: %w", err)
	}

	// Update branch slot and workspace metadata
	if meta.SlotID != "" {
		_ = slotsClient.Update(ctx, meta.SlotID, newHead, currentHeadHash, nil)
	}
	meta.CommitHash = newHead
	_ = WriteWorkspaceMetadata(wsRoot, meta)

	// Materialize new HEAD onto workspace disk
	newCommit, err := commitSvc.GetCommit(ctx, newHead)
	if err == nil && newCommit != nil {
		_ = MaterializeTree(ctx, content.ContentLink{Address: newCommit.Tree.Address}, wsRoot, store)
	}

	return newHead, nil
}

// ExecuteSquashCommit folds uncommitted working tree changes directly into a target commit in history.
func ExecuteSquashCommit(ctx context.Context, store storage.Storage, slotsClient slots.Slots, commitSvc commit.Service, workspaceDir, targetCommitHash string) (string, error) {
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

	targetCommit, err := commitSvc.GetCommit(ctx, targetCommitHash)
	if err != nil {
		return "", fmt.Errorf("target squash commit %s not found: %w", targetCommitHash, err)
	}

	// Snapshot current working tree
	snapshotLink, err := SnapshotDirectory(ctx, wsRoot, store)
	if err != nil {
		return "", fmt.Errorf("failed to snapshot working tree: %w", err)
	}

	// Create temporary squash commit
	squashCommit, squashHash, err := commitSvc.CreateCommit(ctx, commit.CreateRequest{
		RepoName:   meta.RepoName,
		BranchName: meta.BranchName,
		TreeLink:   snapshotLink,
		Parents:    []string{currentHeadHash},
		Message:    fmt.Sprintf("squash! %s", strings.Split(targetCommit.Message, "\n")[0]),
		Author:     targetCommit.Author,
	})
	if err != nil {
		return "", err
	}
	_ = squashCommit

	// Run interactive rebase folding squash commit into target
	baseCommit := meta.ParentSnapshot
	if baseCommit == "" {
		baseCommit = targetCommitHash
		if len(targetCommit.Parents) > 0 {
			baseCommit = targetCommit.Parents[0]
		}
	}

	plan := []commit.RebaseAction{
		{Type: commit.RebasePick, CommitHash: targetCommitHash},
		{Type: commit.RebaseSquash, CommitHash: squashHash},
	}

	newHead, err := commitSvc.InteractiveRebase(ctx, meta.RepoName, meta.BranchName, baseCommit, plan)
	if err != nil {
		return "", fmt.Errorf("squash rebase failed: %w", err)
	}

	if meta.SlotID != "" {
		_ = slotsClient.Update(ctx, meta.SlotID, newHead, currentHeadHash, nil)
	}
	meta.CommitHash = newHead
	_ = WriteWorkspaceMetadata(wsRoot, meta)

	return newHead, nil
}
