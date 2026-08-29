package repository

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"invariant/internal/content"
	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
	"invariant/internal/workspace"
)

// CherryPickOptions specifies parameters for cherry-picking commits.
type CherryPickOptions struct {
	WorkspaceDir string
	Target       string // Branch name or commit hash
	EndCommit    string // Optional end commit for range
}

// ExecuteCherryPick applies commits from a branch or commit range onto the current branch.
func ExecuteCherryPick(ctx context.Context, store storage.Storage, slotsClient slots.Slots, namesClient names.Names, commitSvc commit.Service, opts CherryPickOptions) ([]string, error) {
	wsRoot, meta, err := FindWorkspaceRoot(opts.WorkspaceDir)
	if err != nil {
		return nil, err
	}

	currentHeadHash := meta.CommitHash
	if meta.SlotID != "" {
		slotAddr, err := slotsClient.Get(ctx, meta.SlotID)
		if err == nil && slotAddr != "" {
			currentHeadHash = slotAddr
		}
	}

	// Resolve target commits to cherry-pick
	var commitsToPick []string

	target := strings.TrimSpace(opts.Target)
	if target == "" {
		return nil, fmt.Errorf("cherry-pick requires a branch name or commit hash")
	}

	if opts.EndCommit != "" {
		// Range: startCommit to endCommit
		endCommit, err := commitSvc.GetCommit(ctx, opts.EndCommit)
		if err != nil {
			return nil, fmt.Errorf("invalid end commit %s: %w", opts.EndCommit, err)
		}
		_ = endCommit

		commits, hashes, err := commitSvc.GetHistory(ctx, opts.EndCommit, true, "")
		if err != nil {
			return nil, err
		}

		for i, h := range hashes {
			commitsToPick = append([]string{h}, commitsToPick...)
			if h == target {
				break
			}
			_ = commits[i]
		}
	} else {
		// Check if target is a branch name registered in Names or a commit hash
		resolvedSlot, err := ResolveBranchSlot(ctx, namesClient, meta.RepoName, target)
		if err == nil && resolvedSlot != "" {
			slotAddr, err := slotsClient.Get(ctx, resolvedSlot)
			if err == nil && slotAddr != "" {
				// Pick commits from target branch until convergence with current branch
				_, branchHashes, err := commitSvc.GetHistory(ctx, slotAddr, true, "")
				if err == nil {
					currCommits, currHashes, _ := commitSvc.GetHistory(ctx, currentHeadHash, false, "")
					_ = currCommits
					currMap := make(map[string]bool)
					for _, ch := range currHashes {
						currMap[ch] = true
					}

					for _, bh := range branchHashes {
						if currMap[bh] {
							break
						}
						commitsToPick = append([]string{bh}, commitsToPick...)
					}
				}
			}
		}

		if len(commitsToPick) == 0 {
			// Assume target is a single commit hash
			c, err := commitSvc.GetCommit(ctx, target)
			if err != nil {
				return nil, fmt.Errorf("target commit or branch %q not found: %w", target, err)
			}
			_ = c
			commitsToPick = []string{target}
		}
	}

	if len(commitsToPick) == 0 {
		return nil, fmt.Errorf("no commits found to cherry-pick")
	}

	var createdCommits []string

	for _, commitHash := range commitsToPick {
		sourceCommit, err := commitSvc.GetCommit(ctx, commitHash)
		if err != nil {
			return createdCommits, fmt.Errorf("failed to load commit %s: %w", commitHash, err)
		}

		var parentTreeAddr string
		if len(sourceCommit.Parents) > 0 {
			parentCommit, err := commitSvc.GetCommit(ctx, sourceCommit.Parents[0])
			if err != nil {
				return createdCommits, fmt.Errorf("failed to load parent of %s: %w", commitHash, err)
			}
			parentTreeAddr = parentCommit.Tree.Address
		} else {
			emptyTreeLink, err := CreateEmptyTree(ctx, store)
			if err != nil {
				return createdCommits, err
			}
			parentTreeAddr = emptyTreeLink.Address
		}

		headCommit, err := commitSvc.GetCommit(ctx, currentHeadHash)
		if err != nil {
			return createdCommits, fmt.Errorf("failed to load current HEAD %s: %w", currentHeadHash, err)
		}

		mergedTreeAddr, conflicts, err := workspace.MergeTrees(
			ctx,
			parentTreeAddr,
			headCommit.Tree.Address,
			sourceCommit.Tree.Address,
			store,
			slotsClient,
		)
		if err != nil {
			return createdCommits, fmt.Errorf("cherry-pick merge calculation failed: %w", err)
		}

		if len(conflicts) > 0 {
			return createdCommits, fmt.Errorf("cherry-pick conflict on commit %s: %v", commitHash, conflicts)
		}

		// Create cherry-picked commit
		newRefs := make(map[string]string)
		maps.Copy(newRefs, sourceCommit.Refs)
		newRefs["cherry-picked-from"] = commitHash

		newCommit, newHash, err := commitSvc.CreateCommit(ctx, commit.CreateRequest{
			RepoName:   meta.RepoName,
			BranchName: meta.BranchName,
			TreeLink:   content.ContentLink{Address: mergedTreeAddr},
			Parents:    []string{currentHeadHash},
			Message:    sourceCommit.Message,
			Author:     sourceCommit.Author,
			Tags:       sourceCommit.Tags,
			Refs:       newRefs,
		})
		if err != nil {
			return createdCommits, fmt.Errorf("failed to create cherry-picked commit: %w", err)
		}
		_ = newCommit

		if meta.SlotID != "" {
			_ = slotsClient.Update(ctx, meta.SlotID, newHash, currentHeadHash, nil)
		}
		currentHeadHash = newHash
		createdCommits = append(createdCommits, newHash)
	}

	meta.CommitHash = currentHeadHash
	_ = WriteWorkspaceMetadata(wsRoot, meta)

	// Materialize latest cherry-picked commit onto disk
	latestCommit, err := commitSvc.GetCommit(ctx, currentHeadHash)
	if err == nil && latestCommit != nil {
		_ = MaterializeTree(ctx, content.ContentLink{Address: latestCommit.Tree.Address}, wsRoot, store)
	}

	return createdCommits, nil
}
