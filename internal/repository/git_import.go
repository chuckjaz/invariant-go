package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"invariant/internal/content"
	"invariant/internal/filetree"
	"invariant/internal/kv"
	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/utils/merkletrie"
)

// GitImportOptions configures the import of a Git repository.
type GitImportOptions struct {
	GitDir             string
	Branch             string // Git branch to import and target Invariant branch name
	TargetWorkspaceDir string
	RepoName           string
	Depth              int
	ShowProgress       bool
	ProgressWriter     io.Writer
	RepositoryName     string // If specified, creates repo if not existing, or adds branch if repo exists
	CreateRepoName     string // Deprecated alias for RepositoryName
	Writable           bool
}

// GitImportResult contains summary information about an imported Git repository.
type GitImportResult struct {
	ImportedCommits int                 `json:"importedCommits"`
	RootCommit      string              `json:"rootCommit"`
	HeadCommit      string              `json:"headCommit"`
	HeadCommitLink  content.ContentLink `json:"headCommitLink"`
	BranchName      string              `json:"branchName"`
	RepositoryName  string              `json:"repositoryName,omitempty"`
	CreatedBranch   string              `json:"createdBranch,omitempty"`
	CreatedRepo     bool                `json:"createdRepo,omitempty"`
	UpdatedBranch   bool                `json:"updatedBranch,omitempty"`
	AlreadyUpToDate bool                `json:"alreadyUpToDate,omitempty"`
	CreatedRepoName string              `json:"createdRepoName,omitempty"`
}

// GitImportProgressTracker tracks real-time progress of a Git import operation.
type GitImportProgressTracker struct {
	TotalCommits       int
	CurrentCommitIndex int
	CurrentCommitHash  string
	CurrentCommitMsg   string
	CommitsImported    uint64
	CommitsSkipped     uint64

	FilesChecking int64
	FilesChecked  uint64
	FilesSkipped  uint64
	DirsChecking  int64
	DirsChecked   uint64
	DirsSkipped   uint64
	BytesUploaded uint64

	mu sync.RWMutex
}

// SetCommit updates the current commit being processed by the tracker.
func (t *GitImportProgressTracker) SetCommit(index, total int, hash, message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.CurrentCommitIndex = index
	t.TotalCommits = total
	t.CurrentCommitHash = hash
	t.CurrentCommitMsg = message
}

func (t *GitImportProgressTracker) formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// Start launches a background goroutine printing live import status similar to invariant upload.
func (t *GitImportProgressTracker) Start(ctx context.Context, w io.Writer) func() {
	if w == nil {
		w = os.Stdout
	}
	ctx, cancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		var lastBytes uint64
		lastTime := time.Now()

		for {
			select {
			case <-ctx.Done():
				fmt.Fprintln(w)
				return
			case now := <-ticker.C:
				bytes := atomic.LoadUint64(&t.BytesUploaded)
				deltaBytes := bytes - lastBytes
				deltaTime := now.Sub(lastTime).Seconds()

				bps := float64(0)
				if deltaTime > 0 {
					bps = float64(deltaBytes) / deltaTime
				}

				t.mu.RLock()
				cIdx := t.CurrentCommitIndex
				cTotal := t.TotalCommits
				cHash := t.CurrentCommitHash
				cMsg := t.CurrentCommitMsg
				t.mu.RUnlock()

				fchk := atomic.LoadInt64(&t.FilesChecking)
				fc := atomic.LoadUint64(&t.FilesChecked)
				fs := atomic.LoadUint64(&t.FilesSkipped)
				dchk := atomic.LoadInt64(&t.DirsChecking)
				dc := atomic.LoadUint64(&t.DirsChecked)
				ds := atomic.LoadUint64(&t.DirsSkipped)

				shortHash := cHash
				if len(shortHash) > 8 {
					shortHash = shortHash[:8]
				}
				shortMsg := strings.TrimSpace(cMsg)
				if idx := strings.IndexByte(shortMsg, '\n'); idx >= 0 {
					shortMsg = shortMsg[:idx]
				}
				if len(shortMsg) > 28 {
					shortMsg = shortMsg[:25] + "..."
				}

				commitStr := ""
				if cTotal > 0 {
					if shortMsg != "" {
						commitStr = fmt.Sprintf("Commit: [%d/%d] %s (%q) | ", cIdx, cTotal, shortHash, shortMsg)
					} else {
						commitStr = fmt.Sprintf("Commit: [%d/%d] %s | ", cIdx, cTotal, shortHash)
					}
				}

				fmt.Fprintf(w, "\r\033[K%sFiles: %d checking, %d done, %d skipped | Dirs: %d checking, %d done, %d skipped | Total: %s | Speed: %s/s",
					commitStr, fchk, fc, fs, dchk, dc, ds, t.formatBytes(bytes), t.formatBytes(uint64(bps)))

				lastBytes = bytes
				lastTime = now
			}
		}
	}()

	return func() {
		cancel()
		<-doneCh
	}
}

type trackingReader struct {
	r       io.Reader
	tracker *GitImportProgressTracker
}

func (tr *trackingReader) Read(p []byte) (int, error) {
	n, err := tr.r.Read(p)
	if n > 0 && tr.tracker != nil {
		atomic.AddUint64(&tr.tracker.BytesUploaded, uint64(n))
	}
	return n, err
}

// ImportGitRepository imports Git commit history and file trees into Invariant CAS storage.
func ImportGitRepository(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	namesClient names.Names,
	commitSvc commit.Service,
	kvClient kv.BatchKeyValueStore,
	opts GitImportOptions,
) (*GitImportResult, error) {
	gitPath := opts.GitDir
	if gitPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		gitPath = cwd
	}

	// 1. Open Git repository
	gitRepo, err := git.PlainOpen(gitPath)
	if err != nil {
		gitRepo, err = git.PlainOpenWithOptions(gitPath, &git.PlainOpenOptions{DetectDotGit: true})
		if err != nil {
			return nil, fmt.Errorf("failed to open git repository at %s: %w", gitPath, err)
		}
	}

	// 2. Resolve target branch / commit revision
	var targetHash *plumbing.Hash
	branchName := opts.Branch
	if branchName != "" {
		refName := plumbing.ReferenceName("refs/heads/" + branchName)
		if ref, err := gitRepo.Reference(refName, true); err == nil {
			h := ref.Hash()
			targetHash = &h
		} else if revHash, err := gitRepo.ResolveRevision(plumbing.Revision(branchName)); err == nil {
			targetHash = revHash
		}
	}

	if targetHash == nil {
		headRef, err := gitRepo.Head()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve HEAD in git repository: %w", err)
		}
		h := headRef.Hash()
		targetHash = &h
		if branchName == "" {
			branchName = headRef.Name().Short()
		}
	}

	kvIdx := NewGitKVIndex(kvClient)
	gitToInvCommit := make(map[string]string)
	var rootCommit string
	var headCommit string

	// 3. Collect Git commits in topological order (roots to HEAD), stopping at already-imported commits
	var gitCommits []*object.Commit
	visited := make(map[plumbing.Hash]bool)

	type queueItem struct {
		hash  plumbing.Hash
		depth int
	}
	queue := []queueItem{{hash: *targetHash, depth: 1}}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		if visited[item.hash] {
			continue
		}
		visited[item.hash] = true

		itemHashStr := item.hash.String()
		// Check if commit has already been imported into Invariant
		if existingInv, err := kvIdx.GetCommitInvariantHash(ctx, itemHashStr); err == nil && existingInv != "" {
			if existingCommit, err := commitSvc.GetCommit(ctx, existingInv); err == nil {
				if _, _, ok := checkExistingTreeTimestamps(ctx, store, existingCommit.Tree.Address); ok {
					gitToInvCommit[itemHashStr] = existingInv
					if headCommit == "" && item.hash == *targetHash {
						headCommit = existingInv
						rootCommit = existingInv
					}
					// Assume all predecessor commits are already converted and imported; stop walking this ancestor path!
					continue
				}
			}
		}

		c, err := gitRepo.CommitObject(item.hash)
		if err != nil {
			return nil, fmt.Errorf("failed to load git commit %s: %w", item.hash, err)
		}
		gitCommits = append(gitCommits, c)

		if opts.Depth <= 0 || item.depth < opts.Depth {
			for _, pHash := range c.ParentHashes {
				queue = append(queue, queueItem{hash: pHash, depth: item.depth + 1})
			}
		}
	}

	// Reverse so root commits come first
	for i, j := 0, len(gitCommits)-1; i < j; i, j = i+1, j-1 {
		gitCommits[i], gitCommits[j] = gitCommits[j], gitCommits[i]
	}

	var tracker *GitImportProgressTracker
	if len(gitCommits) > 0 && (opts.ShowProgress || opts.ProgressWriter != nil) {
		tracker = &GitImportProgressTracker{}
		stopProgress := tracker.Start(ctx, opts.ProgressWriter)
		defer stopProgress()
	}

	fileMtimes := make(map[string]uint64)
	fileCtimes := make(map[string]uint64)

	// 4. Import new commits and trees into CAS
	for i, gc := range gitCommits {
		gHashStr := gc.Hash.String()
		if tracker != nil {
			tracker.SetCommit(i+1, len(gitCommits), gHashStr, gc.Message)
		}

		commitTime := uint64(gc.Author.When.Unix())
		if commitTime == 0 {
			commitTime = uint64(gc.Committer.When.Unix())
		}
		if commitTime == 0 {
			commitTime = uint64(time.Now().Unix())
		}

		if currTree, err := gc.Tree(); err == nil {
			if len(gc.ParentHashes) == 0 {
				_ = currTree.Files().ForEach(func(f *object.File) error {
					fileMtimes[f.Name] = commitTime
					fileCtimes[f.Name] = commitTime
					return nil
				})
			} else {
				if parentCommit, err := gitRepo.CommitObject(gc.ParentHashes[0]); err == nil {
					if parentTree, err := parentCommit.Tree(); err == nil {
						if changes, err := object.DiffTree(parentTree, currTree); err == nil {
							for _, ch := range changes {
								action, _ := ch.Action()
								switch action {
								case merkletrie.Insert:
									fileMtimes[ch.To.Name] = commitTime
									fileCtimes[ch.To.Name] = commitTime
								case merkletrie.Modify:
									fileMtimes[ch.To.Name] = commitTime
									if _, ok := fileCtimes[ch.To.Name]; !ok {
										fileCtimes[ch.To.Name] = commitTime
									}
								case merkletrie.Delete:
									delete(fileMtimes, ch.From.Name)
									delete(fileCtimes, ch.From.Name)
								}
							}
						}
					}
				}
				_ = currTree.Files().ForEach(func(f *object.File) error {
					if _, ok := fileMtimes[f.Name]; !ok {
						fileMtimes[f.Name] = commitTime
						fileCtimes[f.Name] = commitTime
					}
					return nil
				})
			}
		}

		// Check if already mapped
		if existingInv, err := kvIdx.GetCommitInvariantHash(ctx, gHashStr); err == nil && existingInv != "" {
			if existingCommit, err := commitSvc.GetCommit(ctx, existingInv); err == nil {
				if _, _, ok := checkExistingTreeTimestamps(ctx, store, existingCommit.Tree.Address); ok {
					gitToInvCommit[gHashStr] = existingInv
					if rootCommit == "" {
						rootCommit = existingInv
					}
					headCommit = existingInv
					if tracker != nil {
						atomic.AddUint64(&tracker.CommitsSkipped, 1)
					}
					continue
				}
			}
		}

		// Import tree
		treeLink, _, _, err := importGitTree(ctx, gitRepo, gc.TreeHash, "", store, kvIdx, tracker, fileMtimes, fileCtimes, commitTime)
		if err != nil {
			return nil, fmt.Errorf("failed to import tree for git commit %s: %w", gHashStr, err)
		}

		// Map parents
		var invParents []string
		for _, p := range gc.ParentHashes {
			if invP, ok := gitToInvCommit[p.String()]; ok {
				invParents = append(invParents, invP)
			}
		}

		author := Identity{
			Name:  gc.Author.Name,
			Email: gc.Author.Email,
		}

		commitObj := &Commit{
			Tree:      treeLink,
			Parents:   invParents,
			Author:    author,
			Message:   gc.Message,
			Timestamp: int64(commitTime),
			Tags: map[string]string{
				"git-commit": gHashStr,
			},
		}

		invHash, err := WriteCommit(ctx, store, commitObj)
		if err != nil {
			return nil, fmt.Errorf("failed to write invariant commit for git commit %s: %w", gHashStr, err)
		}

		gitToInvCommit[gHashStr] = invHash
		_ = kvIdx.RecordCommitMapping(ctx, gHashStr, invHash)
		if tracker != nil {
			atomic.AddUint64(&tracker.CommitsImported, 1)
		}

		if rootCommit == "" {
			rootCommit = invHash
		}
		headCommit = invHash
	}

	res := &GitImportResult{
		ImportedCommits: len(gitCommits),
		RootCommit:      rootCommit,
		HeadCommit:      headCommit,
		HeadCommitLink:  content.ContentLink{Address: headCommit},
		BranchName:      branchName,
	}

	// 5. Create repository or add branch or update workspace
	repoName := opts.RepositoryName
	if repoName == "" {
		repoName = opts.CreateRepoName
	}

	targetBranch := opts.Branch
	if targetBranch == "" {
		targetBranch = branchName
	}
	if targetBranch == "" {
		targetBranch = "main"
	}

	if repoName != "" && headCommit != "" {
		targetDir := opts.TargetWorkspaceDir

		// Check if repository already exists in Names service
		repoEntry, err := namesClient.Get(ctx, repoName)
		repoExists := (err == nil && repoEntry.Value != "")

		if repoExists {
			// Check if target branch already exists in this repository
			var branchSlotID string
			if targetBranch == "main" {
				branchSlotID = repoEntry.Value
				if branchSlotID == "" {
					if bMain, errM := namesClient.Get(ctx, repoName+":main"); errM == nil {
						branchSlotID = bMain.Value
					}
				}
			} else {
				bEntry, err := namesClient.Get(ctx, repoName+":"+targetBranch)
				if err == nil && bEntry.Value != "" {
					branchSlotID = bEntry.Value
				}
			}

			if branchSlotID == "" {
				// Branch does not exist; add the new branch to the existing repository
				allocatedSlotID, err := AllocateSlot(ctx, slotsClient, headCommit, "")
				if err != nil {
					return nil, fmt.Errorf("failed to allocate slot for branch %q: %w", targetBranch, err)
				}
				_ = namesClient.Put(ctx, repoName+":"+targetBranch, allocatedSlotID, nil)

				// Only set up workspace directory if TargetWorkspaceDir is supplied
				if targetDir != "" {
					branchWsDir := filepath.Join(targetDir, targetBranch)
					if err := os.MkdirAll(branchWsDir, 0755); err != nil {
						return nil, fmt.Errorf("failed to create branch workspace directory %s: %w", branchWsDir, err)
					}

					headCommitObj, err := commitSvc.GetCommit(ctx, headCommit)
					if err != nil {
						return nil, fmt.Errorf("failed to retrieve head commit %s: %w", headCommit, err)
					}
					if err := MaterializeTree(ctx, headCommitObj.Tree, branchWsDir, store); err != nil {
						return nil, fmt.Errorf("failed to materialize branch tree in %s: %w", branchWsDir, err)
					}

					meta := &WorkspaceMetadata{
						RepoName:     repoName,
						BranchName:   targetBranch,
						Upstream:     targetBranch,
						SlotID:       allocatedSlotID,
						CommitHash:   headCommit,
						Writable:     opts.Writable,
						CreatedAt:    time.Now().Unix(),
						WorkspaceDir: branchWsDir,
					}
					if err := WriteWorkspaceMetadata(branchWsDir, meta); err != nil {
						return nil, err
					}
					_ = ChangeWorkingDirectory(branchWsDir)
				}

				res.RepositoryName = repoName
				res.CreatedRepoName = repoName
				res.CreatedBranch = targetBranch
				res.CreatedRepo = false
			} else {
				// Branch already exists! Check if the imported commit has the existing branch commit as a predecessor.
				currentCommit, err := slotsClient.Get(ctx, branchSlotID)
				if err != nil {
					return nil, fmt.Errorf("failed to retrieve commit from branch slot %s: %w", branchSlotID, err)
				}

				if currentCommit == "" || currentCommit == headCommit {
					// Already at headCommit
					if targetDir != "" {
						branchWsDir := filepath.Join(targetDir, targetBranch)
						_ = os.MkdirAll(branchWsDir, 0755)
						headCommitObj, err := commitSvc.GetCommit(ctx, headCommit)
						if err == nil && headCommitObj != nil {
							_ = MaterializeTree(ctx, headCommitObj.Tree, branchWsDir, store)
						}
					}
					res.RepositoryName = repoName
					res.CreatedRepoName = repoName
					res.CreatedBranch = targetBranch
					res.AlreadyUpToDate = true
				} else {
					isPredecessor, err := IsCommitAncestor(ctx, commitSvc, currentCommit, headCommit)
					if err != nil {
						return nil, fmt.Errorf("failed to check commit history for branch %q: %w", targetBranch, err)
					}

					if isPredecessor {
						// Fast-forward: imported commit has the current branch commit as predecessor
						if err := slotsClient.Update(ctx, branchSlotID, headCommit, currentCommit, nil); err != nil {
							return nil, fmt.Errorf("failed to update branch slot %s: %w", branchSlotID, err)
						}

						if targetDir != "" {
							branchWsDir := filepath.Join(targetDir, targetBranch)
							_ = os.MkdirAll(branchWsDir, 0755)
							headCommitObj, err := commitSvc.GetCommit(ctx, headCommit)
							if err == nil && headCommitObj != nil {
								_ = MaterializeTree(ctx, headCommitObj.Tree, branchWsDir, store)
							}
							meta, err := ReadWorkspaceMetadata(branchWsDir)
							if err == nil && meta != nil {
								meta.CommitHash = headCommit
								_ = WriteWorkspaceMetadata(branchWsDir, meta)
							} else {
								meta := &WorkspaceMetadata{
									RepoName:     repoName,
									BranchName:   targetBranch,
									Upstream:     targetBranch,
									SlotID:       branchSlotID,
									CommitHash:   headCommit,
									Writable:     opts.Writable,
									CreatedAt:    time.Now().Unix(),
									WorkspaceDir: branchWsDir,
								}
								_ = WriteWorkspaceMetadata(branchWsDir, meta)
							}
							_ = ChangeWorkingDirectory(branchWsDir)
						}

						res.RepositoryName = repoName
						res.CreatedRepoName = repoName
						res.CreatedBranch = targetBranch
						res.UpdatedBranch = true
					} else {
						// Check if headCommit is an ancestor of currentCommit (branch is already ahead)
						isDescendant, _ := IsCommitAncestor(ctx, commitSvc, headCommit, currentCommit)
						if isDescendant {
							res.RepositoryName = repoName
							res.CreatedRepoName = repoName
							res.CreatedBranch = targetBranch
							res.AlreadyUpToDate = true
						} else {
							// Diverged history: current branch commit is not a predecessor of imported commit
							return nil, fmt.Errorf("cannot fast-forward branch %q in repository %q: current branch commit %s is not a predecessor of imported commit %s", targetBranch, repoName, currentCommit, headCommit)
						}
					}
				}
			}
		} else {
			// Repository does not exist; create repository with targetBranch as initial branch
			createOnly := (targetDir == "")
			_, _, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
				Name:       repoName,
				Branch:     targetBranch,
				Content:    headCommit,
				Writable:   opts.Writable,
				TargetDir:  targetDir,
				CreateOnly: createOnly,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to create repository %q on import: %w", repoName, err)
			}
			res.RepositoryName = repoName
			res.CreatedRepoName = repoName
			res.CreatedBranch = targetBranch
			res.CreatedRepo = true
		}
	} else if opts.TargetWorkspaceDir != "" && headCommit != "" {
		wsRoot, meta, err := FindWorkspaceRoot(opts.TargetWorkspaceDir)
		if err == nil && meta != nil {
			needMaterialize := meta.CommitHash != headCommit
			if meta.SlotID != "" {
				currentAddr, _ := slotsClient.Get(ctx, meta.SlotID)
				if currentAddr != headCommit {
					_ = slotsClient.Update(ctx, meta.SlotID, headCommit, currentAddr, nil)
				}
			}
			if meta.CommitHash != headCommit {
				meta.CommitHash = headCommit
				_ = WriteWorkspaceMetadata(wsRoot, meta)
			}

			// Materialize files in workspace only if commit changed
			if needMaterialize {
				headCommitObj, err := commitSvc.GetCommit(ctx, headCommit)
				if err == nil {
					_ = MaterializeTree(ctx, headCommitObj.Tree, wsRoot, store)
				}
			}
		}
	}

	return res, nil
}

func checkExistingTreeTimestamps(ctx context.Context, store storage.Storage, addr string) (uint64, uint64, bool) {
	if addr == "" {
		return 0, 0, false
	}
	rc, err := content.Read(content.ContentLink{Address: addr}, store, nil)
	if err != nil {
		return 0, 0, false
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return 0, 0, false
	}
	var d filetree.Directory
	if err := json.Unmarshal(data, &d); err != nil {
		return 0, 0, false
	}
	if len(d) == 0 {
		return 0, 0, true
	}
	var minC, maxM uint64
	hasTimes := false
	for _, entry := range d {
		var mtime, ctime *uint64
		switch e := entry.(type) {
		case *filetree.FileEntry:
			mtime = e.ModifyTime
			ctime = e.CreateTime
		case *filetree.DirectoryEntry:
			mtime = e.ModifyTime
			ctime = e.CreateTime
		case *filetree.SymbolicLinkEntry:
			mtime = e.ModifyTime
			ctime = e.CreateTime
		}
		if mtime != nil && *mtime > 0 {
			hasTimes = true
			if *mtime > maxM {
				maxM = *mtime
			}
			if ctime != nil && (minC == 0 || *ctime < minC) {
				minC = *ctime
			}
		}
	}
	if !hasTimes {
		return 0, 0, false
	}
	if minC == 0 {
		minC = maxM
	}
	return minC, maxM, true
}

func importGitTree(
	ctx context.Context,
	gitRepo *git.Repository,
	treeHash plumbing.Hash,
	dirPath string,
	store storage.Storage,
	kvIdx *GitKVIndex,
	tracker *GitImportProgressTracker,
	fileMtimes map[string]uint64,
	fileCtimes map[string]uint64,
	commitTime uint64,
) (content.ContentLink, uint64, uint64, error) {
	if tracker != nil {
		atomic.AddInt64(&tracker.DirsChecking, 1)
		defer atomic.AddInt64(&tracker.DirsChecking, -1)
	}

	tHashStr := treeHash.String()

	// Check KV index for existing conversion with valid timestamps
	if existingAddr, err := kvIdx.GetTreeInvariantAddress(ctx, tHashStr); err == nil && existingAddr != "" {
		if minC, maxM, ok := checkExistingTreeTimestamps(ctx, store, existingAddr); ok {
			if tracker != nil {
				atomic.AddUint64(&tracker.DirsSkipped, 1)
			}
			return content.ContentLink{Address: existingAddr}, minC, maxM, nil
		}
	}

	t, err := gitRepo.TreeObject(treeHash)
	if err != nil {
		return content.ContentLink{}, 0, 0, fmt.Errorf("failed to load git tree %s: %w", tHashStr, err)
	}

	var dir filetree.Directory
	var minCtime, maxMtime uint64
	for _, e := range t.Entries {
		name := e.Name
		if name == ".git" || strings.HasPrefix(name, ".invariant-") || strings.HasPrefix(name, ".ir-") {
			continue
		}

		childPath := name
		if dirPath != "" {
			childPath = dirPath + "/" + name
		}

		if e.Mode == filemode.Dir {
			childLink, subMinC, subMaxM, err := importGitTree(ctx, gitRepo, e.Hash, childPath, store, kvIdx, tracker, fileMtimes, fileCtimes, commitTime)
			if err != nil {
				return content.ContentLink{}, 0, 0, err
			}
			dirModeStr := "0755"
			dir = append(dir, &filetree.DirectoryEntry{
				BaseEntry: filetree.BaseEntry{
					Name:       name,
					Kind:       filetree.DirectoryKind,
					Mode:       &dirModeStr,
					CreateTime: &subMinC,
					ModifyTime: &subMaxM,
				},
				Content: childLink,
			})
			if subMaxM > maxMtime {
				maxMtime = subMaxM
			}
			if minCtime == 0 || (subMinC > 0 && subMinC < minCtime) {
				minCtime = subMinC
			}
		} else if e.Mode == filemode.Symlink {
			mtimeVal := fileMtimes[childPath]
			if mtimeVal == 0 {
				mtimeVal = commitTime
			}
			ctimeVal := fileCtimes[childPath]
			if ctimeVal == 0 {
				ctimeVal = mtimeVal
			}

			blobObj, err := gitRepo.BlobObject(e.Hash)
			if err != nil {
				return content.ContentLink{}, 0, 0, fmt.Errorf("failed to load git symlink blob %s: %w", e.Hash, err)
			}
			reader, err := blobObj.Reader()
			if err != nil {
				return content.ContentLink{}, 0, 0, fmt.Errorf("failed to read git symlink blob %s: %w", e.Hash, err)
			}
			targetBytes, _ := io.ReadAll(reader)
			reader.Close()
			target := string(targetBytes)
			modeStr := "0777"
			dir = append(dir, &filetree.SymbolicLinkEntry{
				BaseEntry: filetree.BaseEntry{
					Name:       name,
					Kind:       filetree.SymbolicLinkKind,
					Mode:       &modeStr,
					CreateTime: &ctimeVal,
					ModifyTime: &mtimeVal,
				},
				Target: target,
			})
			if mtimeVal > maxMtime {
				maxMtime = mtimeVal
			}
			if minCtime == 0 || (ctimeVal > 0 && ctimeVal < minCtime) {
				minCtime = ctimeVal
			}
		} else if e.Mode.IsFile() {
			if tracker != nil {
				atomic.AddInt64(&tracker.FilesChecking, 1)
			}

			mtimeVal := fileMtimes[childPath]
			if mtimeVal == 0 {
				mtimeVal = commitTime
			}
			ctimeVal := fileCtimes[childPath]
			if ctimeVal == 0 {
				ctimeVal = mtimeVal
			}

			fHashStr := e.Hash.String()
			var fileLink content.ContentLink
			var fileSize uint64

			// Check KV index for blob
			if existingBlobSHA256, err := kvIdx.GetBlobInvariantSHA256(ctx, fHashStr); err == nil && existingBlobSHA256 != "" {
				if tracker != nil {
					atomic.AddUint64(&tracker.FilesSkipped, 1)
					atomic.AddInt64(&tracker.FilesChecking, -1)
				}
				fileLink = content.ContentLink{Address: existingBlobSHA256}
				blobObj, err := gitRepo.BlobObject(e.Hash)
				if err == nil {
					fileSize = uint64(blobObj.Size)
				}
			} else {
				blobObj, err := gitRepo.BlobObject(e.Hash)
				if err != nil {
					if tracker != nil {
						atomic.AddInt64(&tracker.FilesChecking, -1)
					}
					return content.ContentLink{}, 0, 0, fmt.Errorf("failed to load git blob %s: %w", fHashStr, err)
				}
				fileSize = uint64(blobObj.Size)

				reader, err := blobObj.Reader()
				if err != nil {
					if tracker != nil {
						atomic.AddInt64(&tracker.FilesChecking, -1)
					}
					return content.ContentLink{}, 0, 0, fmt.Errorf("failed to read git blob %s: %w", fHashStr, err)
				}

				var r io.Reader = reader
				if tracker != nil {
					r = &trackingReader{r: reader, tracker: tracker}
				}

				link, err := content.Write(r, store, content.WriterOptions{})
				reader.Close()
				if tracker != nil {
					atomic.AddInt64(&tracker.FilesChecking, -1)
				}
				if err != nil {
					return content.ContentLink{}, 0, 0, fmt.Errorf("failed to write blob %s to CAS: %w", fHashStr, err)
				}
				if tracker != nil {
					atomic.AddUint64(&tracker.FilesChecked, 1)
				}
				fileLink = link
				_ = kvIdx.RecordBlobMapping(ctx, fHashStr, fileLink.Address)
			}

			modeStr := fmt.Sprintf("%04o", uint32(e.Mode&07777))
			dir = append(dir, &filetree.FileEntry{
				BaseEntry: filetree.BaseEntry{
					Name:       name,
					Kind:       filetree.FileKind,
					Mode:       &modeStr,
					CreateTime: &ctimeVal,
					ModifyTime: &mtimeVal,
				},
				Content: fileLink,
				Size:    fileSize,
			})
			if mtimeVal > maxMtime {
				maxMtime = mtimeVal
			}
			if minCtime == 0 || (ctimeVal > 0 && ctimeVal < minCtime) {
				minCtime = ctimeVal
			}
		}
	}

	if maxMtime == 0 {
		maxMtime = commitTime
	}
	if minCtime == 0 {
		minCtime = maxMtime
	}

	dirData, err := json.Marshal(dir)
	if err != nil {
		return content.ContentLink{}, 0, 0, fmt.Errorf("failed to marshal directory: %w", err)
	}

	link, err := content.Write(bytes.NewReader(dirData), store, content.WriterOptions{})
	if err != nil {
		return content.ContentLink{}, 0, 0, fmt.Errorf("failed to write directory tree to CAS: %w", err)
	}

	if tracker != nil {
		atomic.AddUint64(&tracker.DirsChecked, 1)
	}

	_ = kvIdx.RecordTreeMapping(ctx, tHashStr, link.Address)
	return link, minCtime, maxMtime, nil
}

// IsCommitAncestor checks whether ancestorHash is reachable from descendantHash in the Invariant commit DAG.
func IsCommitAncestor(ctx context.Context, commitSvc commit.Service, ancestorHash, descendantHash string) (bool, error) {
	if ancestorHash == "" || descendantHash == "" {
		return false, nil
	}
	if ancestorHash == descendantHash {
		return true, nil
	}

	visited := make(map[string]bool)
	queue := []string{descendantHash}
	visited[descendantHash] = true

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		if curr == ancestorHash {
			return true, nil
		}

		c, err := commitSvc.GetCommit(ctx, curr)
		if err != nil || c == nil {
			continue
		}

		for _, p := range c.Parents {
			if !visited[p] {
				visited[p] = true
				queue = append(queue, p)
			}
		}
	}

	return false, nil
}
