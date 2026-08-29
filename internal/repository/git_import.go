package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
)

// GitImportOptions configures the import of a Git repository.
type GitImportOptions struct {
	GitDir             string
	Branch             string
	TargetWorkspaceDir string
	RepoName           string
	Depth              int
}

// GitImportResult contains summary information about an imported Git repository.
type GitImportResult struct {
	ImportedCommits int    `json:"importedCommits"`
	RootCommit      string `json:"rootCommit"`
	HeadCommit      string `json:"headCommit"`
	BranchName      string `json:"branchName"`
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

	// 3. Collect Git commits in topological order (roots to HEAD)
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

	kvIdx := NewGitKVIndex(kvClient)
	gitToInvCommit := make(map[string]string)
	var rootCommit string
	var headCommit string

	// 4. Import commits and trees into CAS
	for _, gc := range gitCommits {
		gHashStr := gc.Hash.String()

		// Check if already mapped
		if existingInv, err := kvIdx.GetCommitInvariantHash(ctx, gHashStr); err == nil && existingInv != "" {
			if _, err := commitSvc.GetCommit(ctx, existingInv); err == nil {
				gitToInvCommit[gHashStr] = existingInv
				if rootCommit == "" {
					rootCommit = existingInv
				}
				headCommit = existingInv
				continue
			}
		}

		// Import tree
		treeLink, err := importGitTree(ctx, gitRepo, gc.TreeHash, store, kvIdx)
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
			Timestamp: gc.Author.When.Unix(),
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

		if rootCommit == "" {
			rootCommit = invHash
		}
		headCommit = invHash
	}

	// 5. Update workspace if target workspace directory is specified
	if opts.TargetWorkspaceDir != "" {
		wsRoot, meta, err := FindWorkspaceRoot(opts.TargetWorkspaceDir)
		if err == nil && meta != nil {
			// Update branch slot
			if meta.SlotID != "" && headCommit != "" {
				_ = slotsClient.Update(ctx, meta.SlotID, headCommit, meta.CommitHash, nil)
			}
			meta.CommitHash = headCommit
			meta.ParentSnapshot = headCommit
			_ = WriteWorkspaceMetadata(wsRoot, meta)

			// Materialize tree
			headCommitObj, err := commitSvc.GetCommit(ctx, headCommit)
			if err == nil {
				_ = MaterializeTree(ctx, headCommitObj.Tree, wsRoot, store)
			}
		}
	}

	return &GitImportResult{
		ImportedCommits: len(gitCommits),
		RootCommit:      rootCommit,
		HeadCommit:      headCommit,
		BranchName:      branchName,
	}, nil
}

func importGitTree(
	ctx context.Context,
	gitRepo *git.Repository,
	treeHash plumbing.Hash,
	store storage.Storage,
	kvIdx *GitKVIndex,
) (content.ContentLink, error) {
	tHashStr := treeHash.String()

	// Check KV index for existing conversion
	if existingAddr, err := kvIdx.GetTreeInvariantAddress(ctx, tHashStr); err == nil && existingAddr != "" {
		return content.ContentLink{Address: existingAddr}, nil
	}

	t, err := gitRepo.TreeObject(treeHash)
	if err != nil {
		return content.ContentLink{}, fmt.Errorf("failed to load git tree %s: %w", tHashStr, err)
	}

	var dir filetree.Directory
	for _, e := range t.Entries {
		name := e.Name
		if name == ".git" || strings.HasPrefix(name, ".invariant-") || strings.HasPrefix(name, ".ir-") {
			continue
		}

		if e.Mode == filemode.Dir {
			childLink, err := importGitTree(ctx, gitRepo, e.Hash, store, kvIdx)
			if err != nil {
				return content.ContentLink{}, err
			}
			dir = append(dir, &filetree.DirectoryEntry{
				BaseEntry: filetree.BaseEntry{
					Name: name,
					Kind: filetree.DirectoryKind,
				},
				Content: childLink,
			})
		} else if e.Mode.IsFile() {
			fHashStr := e.Hash.String()
			var fileLink content.ContentLink
			var fileSize uint64

			// Check KV index for blob
			if existingBlobSHA256, err := kvIdx.GetBlobInvariantSHA256(ctx, fHashStr); err == nil && existingBlobSHA256 != "" {
				fileLink = content.ContentLink{Address: existingBlobSHA256}
				blobObj, err := gitRepo.BlobObject(e.Hash)
				if err == nil {
					fileSize = uint64(blobObj.Size)
				}
			} else {
				blobObj, err := gitRepo.BlobObject(e.Hash)
				if err != nil {
					return content.ContentLink{}, fmt.Errorf("failed to load git blob %s: %w", fHashStr, err)
				}
				fileSize = uint64(blobObj.Size)

				reader, err := blobObj.Reader()
				if err != nil {
					return content.ContentLink{}, fmt.Errorf("failed to read git blob %s: %w", fHashStr, err)
				}

				link, err := content.Write(reader, store, content.WriterOptions{})
				reader.Close()
				if err != nil {
					return content.ContentLink{}, fmt.Errorf("failed to write blob %s to CAS: %w", fHashStr, err)
				}
				fileLink = link
				_ = kvIdx.RecordBlobMapping(ctx, fHashStr, fileLink.Address)
			}

			modeStr := fmt.Sprintf("%04o", uint32(e.Mode))
			dir = append(dir, &filetree.FileEntry{
				BaseEntry: filetree.BaseEntry{
					Name: name,
					Kind: filetree.FileKind,
					Mode: &modeStr,
				},
				Content: fileLink,
				Size:    fileSize,
			})
		}
	}

	dirData, err := json.Marshal(dir)
	if err != nil {
		return content.ContentLink{}, fmt.Errorf("failed to marshal directory: %w", err)
	}

	link, err := content.Write(bytes.NewReader(dirData), store, content.WriterOptions{})
	if err != nil {
		return content.ContentLink{}, fmt.Errorf("failed to write directory tree to CAS: %w", err)
	}

	_ = kvIdx.RecordTreeMapping(ctx, tHashStr, link.Address)
	return link, nil
}
