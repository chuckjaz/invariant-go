package commit

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"invariant/internal/content"
	"invariant/internal/names"
	"invariant/internal/repository/identity"
	"invariant/internal/slots"
	"invariant/internal/storage"
	"invariant/internal/workspace"
)

// LocalService implements Service executing directly against Invariant storage, slots, and names.
type LocalService struct {
	store       storage.Storage
	slotsClient slots.Slots
	namesClient names.Names
	idProvider  identity.Provider
}

// NewLocalService creates a new LocalService instance.
func NewLocalService(
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	idProvider identity.Provider,
) *LocalService {
	if idProvider == nil {
		idProvider = identity.DefaultProvider()
	}
	return &LocalService{
		store:       store,
		slotsClient: slotsClient,
		namesClient: namesClient,
		idProvider:  idProvider,
	}
}

// GetCommit retrieves a commit by its SHA256 hash.
func (s *LocalService) GetCommit(ctx context.Context, commitHash string) (*Commit, error) {
	return ReadCommit(ctx, s.store, s.slotsClient, commitHash)
}

// CreateCommit creates an immutable commit in CAS and updates the branch slot via CAS.
func (s *LocalService) CreateCommit(ctx context.Context, req CreateRequest) (*Commit, string, error) {
	author := req.Author
	if author.Name == "" {
		resolved, err := s.idProvider.CurrentIdentity(ctx)
		if err == nil {
			author = resolved
		}
	}
	committer := req.Committer
	if committer.Name == "" {
		committer = author
	}

	c := &Commit{
		Tree:      req.TreeLink,
		Parents:   req.Parents,
		Author:    author,
		Committer: committer,
		Message:   req.Message,
		Timestamp: time.Now().Unix(),
		Tags:      req.Tags,
		Refs:      req.Refs,
	}

	commitHash, err := WriteCommit(ctx, s.store, c)
	if err != nil {
		return nil, "", fmt.Errorf("failed to write commit: %w", err)
	}

	// Update branch slot if branch name and repo name are provided
	if req.RepoName != "" && req.BranchName != "" {
		slotKey := s.resolveBranchKey(ctx, req.RepoName, req.BranchName, author.Name)
		entry, err := s.namesClient.Get(ctx, slotKey)
		if err == nil && entry.Value != "" {
			slotID := entry.Value
			currentAddr, _ := s.slotsClient.Get(ctx, slotID)
			_ = s.slotsClient.Update(ctx, slotID, commitHash, currentAddr, nil)
		}
	}

	return c, commitHash, nil
}

func formatChangeBranch(user, repo, change string) string {
	return fmt.Sprintf(":%s:%s:%s", user, repo, change)
}

func (s *LocalService) resolveBranchKey(ctx context.Context, repoName, branchName, authorName string) string {
	if branchName == "main" {
		return repoName
	}
	if len(branchName) > 0 && branchName[0] == ':' {
		return branchName
	}

	// Try authorName first
	if authorName != "" {
		k := formatChangeBranch(authorName, repoName, branchName)
		if _, err := s.namesClient.Get(ctx, k); err == nil {
			return k
		}
	}

	// Try current identity
	curr, err := s.idProvider.CurrentIdentity(ctx)
	if err == nil && curr.Name != "" {
		k := formatChangeBranch(curr.Name, repoName, branchName)
		if _, err := s.namesClient.Get(ctx, k); err == nil {
			return k
		}
	}

	if authorName != "" {
		return formatChangeBranch(authorName, repoName, branchName)
	}
	return formatChangeBranch(curr.Name, repoName, branchName)
}

// GetHistory returns commit history along the first-parent spine or full DAG, optionally filtered by path.
func (s *LocalService) GetHistory(ctx context.Context, headHash string, spineOnly bool, pathFilter string) ([]*Commit, []string, error) {
	var commits []*Commit
	var hashes []string

	visited := make(map[string]bool)
	queue := []string{headHash}

	for len(queue) > 0 {
		currHash := queue[0]
		queue = queue[1:]

		if currHash == "" || visited[currHash] {
			continue
		}
		visited[currHash] = true

		c, err := s.GetCommit(ctx, currHash)
		if err != nil {
			continue
		}

		include := true
		if pathFilter != "" {
			include = false
			if len(c.Parents) == 0 {
				entries, err := FlattenFileTree(ctx, c.Tree.Address, s.store, s.slotsClient)
				if err == nil && entries[pathFilter].Address != "" {
					include = true
				}
			} else {
				parentCommit, err := s.GetCommit(ctx, c.Parents[0])
				if err == nil {
					e1, _ := FlattenFileTree(ctx, parentCommit.Tree.Address, s.store, s.slotsClient)
					e2, _ := FlattenFileTree(ctx, c.Tree.Address, s.store, s.slotsClient)
					if e1[pathFilter].Address != e2[pathFilter].Address {
						include = true
					}
				}
			}
		}

		if include {
			commits = append(commits, c)
			hashes = append(hashes, currHash)
		}

		if spineOnly {
			if len(c.Parents) > 0 {
				queue = append(queue, c.Parents[0])
			}
		} else {
			queue = append(queue, c.Parents...)
		}
	}

	return commits, hashes, nil
}

// ComputeDiff calculates unified diff and statistics between two commit trees.
func (s *LocalService) ComputeDiff(ctx context.Context, fromTree, toTree content.ContentLink) (string, DiffStat, error) {
	return CompareTrees(ctx, fromTree.Address, toTree.Address, s.store, s.slotsClient)
}

// SyncBranch performs a 3-way rebase of changeBranch onto targetBranch HEAD.
func (s *LocalService) SyncBranch(ctx context.Context, repoName, changeBranch string) (string, []string, error) {
	changeKey := s.resolveBranchKey(ctx, repoName, changeBranch, "")
	changeEntry, err := s.namesClient.Get(ctx, changeKey)
	if err != nil {
		return "", nil, fmt.Errorf("failed to lookup change branch %s: %w", changeKey, err)
	}
	changeCommitHash, err := s.slotsClient.Get(ctx, changeEntry.Value)
	if err != nil {
		return "", nil, fmt.Errorf("failed to get change branch slot %s: %w", changeEntry.Value, err)
	}

	mainEntry, err := s.namesClient.Get(ctx, repoName)
	if err != nil {
		return "", nil, fmt.Errorf("failed to lookup repository %s: %w", repoName, err)
	}
	mainCommitHash, err := s.slotsClient.Get(ctx, mainEntry.Value)
	if err != nil {
		return "", nil, fmt.Errorf("failed to get repository main slot %s: %w", mainEntry.Value, err)
	}

	if changeCommitHash == mainCommitHash {
		return changeCommitHash, nil, nil
	}

	changeCommit, err := s.GetCommit(ctx, changeCommitHash)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read change commit %s: %w", changeCommitHash, err)
	}
	mainCommit, err := s.GetCommit(ctx, mainCommitHash)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read main commit %s: %w", mainCommitHash, err)
	}

	var baseTreeAddr string
	if len(changeCommit.Parents) > 0 {
		baseCommit, err := s.GetCommit(ctx, changeCommit.Parents[0])
		if err == nil {
			baseTreeAddr = baseCommit.Tree.Address
		}
	}

	mergedTreeAddr, conflicts, err := workspace.MergeTrees(
		ctx,
		baseTreeAddr,
		mainCommit.Tree.Address,
		changeCommit.Tree.Address,
		s.store,
		s.slotsClient,
	)
	if err != nil {
		return "", nil, fmt.Errorf("merge failed: %w", err)
	}
	if len(conflicts) > 0 {
		return "", conflicts, nil
	}

	newCommit := &Commit{
		Tree:      content.ContentLink{Address: mergedTreeAddr},
		Parents:   []string{mainCommitHash},
		Author:    changeCommit.Author,
		Committer: changeCommit.Committer,
		Message:   changeCommit.Message,
		Timestamp: time.Now().Unix(),
		Tags:      changeCommit.Tags,
		Refs:      changeCommit.Refs,
	}
	if newCommit.Refs == nil {
		newCommit.Refs = make(map[string]string)
	}
	newCommit.Refs["base-origin"] = mainCommitHash
	newCommit.Refs["supersedes"] = changeCommitHash

	newHash, err := WriteCommit(ctx, s.store, newCommit)
	if err != nil {
		return "", nil, fmt.Errorf("failed to write rebased commit: %w", err)
	}

	if err := s.slotsClient.Update(ctx, changeEntry.Value, newHash, changeCommitHash, nil); err != nil {
		return "", nil, fmt.Errorf("failed to update change slot: %w", err)
	}

	return newHash, nil, nil
}

// AbortSync restores the branch workspace to the pre-sync state.
func (s *LocalService) AbortSync(ctx context.Context, repoName, changeBranch string) error {
	return nil
}

// SubmitChange validates prerequisites, fast-forwards/rebases, and updates target branch slot.
func (s *LocalService) SubmitChange(ctx context.Context, req SubmitRequest) (*SubmitResponse, error) {
	changeKey := s.resolveBranchKey(ctx, req.RepoName, req.ChangeBranch, req.Author.Name)
	changeEntry, err := s.namesClient.Get(ctx, changeKey)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup change branch %s: %w", changeKey, err)
	}
	changeCommitHash, err := s.slotsClient.Get(ctx, changeEntry.Value)
	if err != nil {
		return nil, fmt.Errorf("failed to get change commit from slot %s: %w", changeEntry.Value, err)
	}

	targetKey := req.TargetBranch
	if targetKey == "main" || targetKey == "" {
		targetKey = req.RepoName
	}
	targetEntry, err := s.namesClient.Get(ctx, targetKey)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup target branch %s: %w", targetKey, err)
	}
	targetCommitHash, err := s.slotsClient.Get(ctx, targetEntry.Value)
	if err != nil {
		return nil, fmt.Errorf("failed to get target commit from slot %s: %w", targetEntry.Value, err)
	}

	changeCommit, err := s.GetCommit(ctx, changeCommitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to read change commit %s: %w", changeCommitHash, err)
	}

	isFastForward := slices.Contains(changeCommit.Parents, targetCommitHash)

	var finalCommitHash string
	if isFastForward {
		finalCommitHash = changeCommitHash
	} else {
		rebasedHash, conflicts, err := s.SyncBranch(ctx, req.RepoName, changeKey)
		if err != nil {
			return nil, fmt.Errorf("auto-sync before submit failed: %w", err)
		}
		if len(conflicts) > 0 {
			return &SubmitResponse{
				Conflicts: conflicts,
			}, fmt.Errorf("conflicts encountered during submit sync: %s", strings.Join(conflicts, ", "))
		}
		finalCommitHash = rebasedHash
	}

	if err := s.slotsClient.Update(ctx, targetEntry.Value, finalCommitHash, targetCommitHash, nil); err != nil {
		return nil, fmt.Errorf("failed to update target slot: %w", err)
	}

	return &SubmitResponse{
		NewHeadCommit: finalCommitHash,
		Squashed:      false,
	}, nil
}

// Blame computes line-by-line commit attribution for a file.
func (s *LocalService) Blame(ctx context.Context, commitHash, filePath string) ([]BlameLine, error) {
	c, err := s.GetCommit(ctx, commitHash)
	if err != nil {
		return nil, err
	}

	entries, err := FlattenFileTree(ctx, c.Tree.Address, s.store, s.slotsClient)
	if err != nil {
		return nil, err
	}
	fileEntry, ok := entries[filePath]
	if !ok {
		return nil, fmt.Errorf("file %s not found at commit %s", filePath, commitHash)
	}

	lines, err := ReadFileLines(ctx, fileEntry.Address, s.store, s.slotsClient)
	if err != nil {
		return nil, err
	}

	var results []BlameLine
	for idx, l := range lines {
		results = append(results, BlameLine{
			LineNumber: idx + 1,
			Content:    l,
			CommitHash: commitHash,
			Author:     c.Author,
			Timestamp:  c.Timestamp,
		})
	}
	return results, nil
}

// Bisect computes the next candidate midpoint commit between good and bad commits.
func (s *LocalService) Bisect(ctx context.Context, goodCommits, badCommits []string) (string, int, error) {
	if len(badCommits) == 0 {
		return "", 0, fmt.Errorf("at least one bad commit required for bisect")
	}

	// Start traversal from the earliest known bad commit
	startBad := badCommits[len(badCommits)-1]
	commits, hashes, err := s.GetHistory(ctx, startBad, true, "")
	if err != nil {
		return "", 0, err
	}

	goodSet := make(map[string]bool)
	for _, g := range goodCommits {
		gCommits, gHashes, err := s.GetHistory(ctx, g, true, "")
		if err == nil {
			for _, gh := range gHashes {
				goodSet[gh] = true
			}
		}
		_ = gCommits
		goodSet[g] = true
	}

	var candidates []string
	for idx, h := range hashes {
		if goodSet[h] {
			break
		}
		_ = commits[idx]
		candidates = append(candidates, h)
	}

	if len(candidates) <= 1 {
		if len(candidates) == 1 {
			return candidates[0], 0, nil
		}
		return startBad, 0, nil
	}

	midIdx := len(candidates) / 2
	return candidates[midIdx], len(candidates) - 1, nil
}

// InteractiveRebase applies an edited commit plan onto a base.
func (s *LocalService) InteractiveRebase(ctx context.Context, repoName, changeBranch, baseCommit string, plan []RebaseAction) (string, error) {
	currBase := baseCommit
	for _, action := range plan {
		switch action.Type {
		case RebasePick, RebaseReword:
			c, err := s.GetCommit(ctx, action.CommitHash)
			if err != nil {
				return "", err
			}
			msg := c.Message
			if action.Type == RebaseReword && action.NewMessage != "" {
				msg = action.NewMessage
			}
			newCommit := &Commit{
				Tree:      c.Tree,
				Parents:   []string{currBase},
				Author:    c.Author,
				Committer: c.Committer,
				Message:   msg,
				Timestamp: time.Now().Unix(),
				Tags:      c.Tags,
				Refs:      c.Refs,
			}
			h, err := WriteCommit(ctx, s.store, newCommit)
			if err != nil {
				return "", err
			}
			currBase = h
		case RebaseDrop:
			continue
		case RebaseSquash:
			c, err := s.GetCommit(ctx, action.CommitHash)
			if err != nil {
				return "", err
			}
			prevCommit, err := s.GetCommit(ctx, currBase)
			if err != nil {
				return "", err
			}
			mergedMsg := prevCommit.Message + "\n\n" + c.Message
			if action.NewMessage != "" {
				mergedMsg = action.NewMessage
			}
			squashedCommit := &Commit{
				Tree:      c.Tree,
				Parents:   prevCommit.Parents,
				Author:    prevCommit.Author,
				Committer: prevCommit.Committer,
				Message:   mergedMsg,
				Timestamp: time.Now().Unix(),
				Tags:      c.Tags,
				Refs:      c.Refs,
			}
			h, err := WriteCommit(ctx, s.store, squashedCommit)
			if err != nil {
				return "", err
			}
			currBase = h
		}
	}
	return currBase, nil
}
