package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"invariant/internal/content"
	"invariant/internal/filetree"
	"invariant/internal/kv"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// GitExportOptions configures the export of Invariant commits to a Git repository.
type GitExportOptions struct {
	WorkspaceDir string
	TargetGitDir string
	Branch       string
	FromCommit   string
}

// GitExportResult contains summary information about an exported Git repository.
type GitExportResult struct {
	ExportedCommits int    `json:"exportedCommits"`
	GitBranch       string `json:"gitBranch"`
	GitHeadCommit   string `json:"gitHeadCommit"`
}

// ExportGitRepository exports Invariant commit history and file trees into a Git repository.
func ExportGitRepository(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	commitSvc commit.Service,
	kvClient kv.BatchKeyValueStore,
	opts GitExportOptions,
) (*GitExportResult, error) {
	if opts.TargetGitDir == "" {
		return nil, fmt.Errorf("target git directory cannot be empty")
	}

	_, meta, err := FindWorkspaceRoot(opts.WorkspaceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to locate workspace: %w", err)
	}

	targetHead := opts.FromCommit
	if targetHead == "" {
		targetHead = meta.CommitHash
		if meta.SlotID != "" {
			if latestHash, err := slotsClient.Get(ctx, meta.SlotID); err == nil && latestHash != "" {
				targetHead = latestHash
			}
		}
	}

	if targetHead == "" {
		return nil, fmt.Errorf("no commit to export")
	}

	// 1. Open or initialize target Git repository
	if err := os.MkdirAll(opts.TargetGitDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create target git directory %s: %w", opts.TargetGitDir, err)
	}

	gitRepo, err := git.PlainOpen(opts.TargetGitDir)
	if err != nil {
		gitRepo, err = git.PlainInit(opts.TargetGitDir, false)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize git repository at %s: %w", opts.TargetGitDir, err)
		}
	}

	// 2. Fetch Invariant history DAG
	invCommits, _, err := commitSvc.GetHistory(ctx, targetHead, false, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get commit history for %s: %w", targetHead, err)
	}

	// Reverse so root commits are processed first
	for i, j := 0, len(invCommits)-1; i < j; i, j = i+1, j-1 {
		invCommits[i], invCommits[j] = invCommits[j], invCommits[i]
	}

	kvIdx := NewGitKVIndex(kvClient)
	invToGitCommit := make(map[string]plumbing.Hash)
	var lastGitCommitHash plumbing.Hash

	// 3. Export commits to Git object store
	for _, ic := range invCommits {
		invHash, _ := CalculateCommitHash(ic)

		// Check if commit is already mapped in KV
		if existingGitSHA1, err := kvIdx.GetCommitGitSHA1(ctx, invHash); err == nil && existingGitSHA1 != "" {
			h := plumbing.NewHash(existingGitSHA1)
			if _, err := gitRepo.CommitObject(h); err == nil {
				invToGitCommit[invHash] = h
				lastGitCommitHash = h
				continue
			}
		}

		// Export tree
		gitTreeHash, err := exportInvariantTree(ctx, store, slotsClient, ic.Tree, gitRepo.Storer, kvIdx)
		if err != nil {
			return nil, fmt.Errorf("failed to export tree for invariant commit %s: %w", invHash, err)
		}

		// Map parents
		var gitParents []plumbing.Hash
		for _, pInv := range ic.Parents {
			if gP, ok := invToGitCommit[pInv]; ok {
				gitParents = append(gitParents, gP)
			}
		}

		// Create git commit object
		authorTime := time.Unix(ic.Timestamp, 0)
		if ic.Timestamp == 0 {
			authorTime = time.Now()
		}

		authorSig := object.Signature{
			Name:  ic.Author.Name,
			Email: ic.Author.Email,
			When:  authorTime,
		}
		if authorSig.Name == "" {
			authorSig.Name = "Invariant User"
		}
		if authorSig.Email == "" {
			authorSig.Email = "invariant@example.com"
		}

		gitCommitObj := &object.Commit{
			Author:       authorSig,
			Committer:    authorSig,
			Message:      ic.Message,
			TreeHash:     gitTreeHash,
			ParentHashes: gitParents,
		}

		encObj := gitRepo.Storer.NewEncodedObject()
		if err := gitCommitObj.Encode(encObj); err != nil {
			return nil, fmt.Errorf("failed to encode git commit: %w", err)
		}

		gitCommitHash, err := gitRepo.Storer.SetEncodedObject(encObj)
		if err != nil {
			return nil, fmt.Errorf("failed to store git commit: %w", err)
		}

		invToGitCommit[invHash] = gitCommitHash
		lastGitCommitHash = gitCommitHash
		_ = kvIdx.RecordCommitMapping(ctx, gitCommitHash.String(), invHash)
	}

	branchName := opts.Branch
	if branchName == "" {
		branchName = "main"
	}

	// 4. Update Git branch reference
	branchRefName := plumbing.ReferenceName("refs/heads/" + branchName)
	branchRef := plumbing.NewHashReference(branchRefName, lastGitCommitHash)
	if err := gitRepo.Storer.SetReference(branchRef); err != nil {
		return nil, fmt.Errorf("failed to set git branch reference %s: %w", branchRefName, err)
	}

	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, branchRefName)
	if err := gitRepo.Storer.SetReference(headRef); err != nil {
		return nil, fmt.Errorf("failed to set HEAD symbolic reference: %w", err)
	}

	// 5. Checkout worktree files if worktree is available
	if wt, err := gitRepo.Worktree(); err == nil {
		_ = wt.Reset(&git.ResetOptions{
			Commit: lastGitCommitHash,
			Mode:   git.HardReset,
		})
	}

	return &GitExportResult{
		ExportedCommits: len(invCommits),
		GitBranch:       branchName,
		GitHeadCommit:   lastGitCommitHash.String(),
	}, nil
}

func exportInvariantTree(
	ctx context.Context,
	store storage.Storage,
	slotsClient slots.Slots,
	treeLink content.ContentLink,
	storer storer.EncodedObjectStorer,
	kvIdx *GitKVIndex,
) (plumbing.Hash, error) {
	if treeLink.Address == "" {
		return plumbing.ZeroHash, nil
	}

	// Check KV index for cached Git tree hash and verify it exists in target storer
	if existingGitTreeSHA1, err := kvIdx.GetTreeGitSHA1(ctx, treeLink.Address); err == nil && existingGitTreeSHA1 != "" {
		h := plumbing.NewHash(existingGitTreeSHA1)
		if _, err := storer.EncodedObject(plumbing.TreeObject, h); err == nil {
			return h, nil
		}
	}

	rc, err := content.Read(treeLink, store, slotsClient)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to read invariant tree %s: %w", treeLink.Address, err)
	}
	defer rc.Close()

	var dir filetree.Directory
	if err := json.NewDecoder(rc).Decode(&dir); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to decode invariant directory %s: %w", treeLink.Address, err)
	}

	var treeEntries []object.TreeEntry

	for _, entry := range dir {
		name := entry.GetName()

		switch entry.GetKind() {
		case filetree.DirectoryKind:
			dirEntry, ok := entry.(*filetree.DirectoryEntry)
			if !ok {
				continue
			}
			childTreeHash, err := exportInvariantTree(ctx, store, slotsClient, dirEntry.Content, storer, kvIdx)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			treeEntries = append(treeEntries, object.TreeEntry{
				Name: name,
				Mode: filemode.Dir,
				Hash: childTreeHash,
			})

		case filetree.FileKind:
			fileEntry, ok := entry.(*filetree.FileEntry)
			if !ok {
				continue
			}

			// Read file content from CAS
			var blobHash plumbing.Hash
			var blobExists bool
			if existingBlobGitSHA1, err := kvIdx.GetBlobGitSHA1(ctx, fileEntry.Content.Address); err == nil && existingBlobGitSHA1 != "" {
				h := plumbing.NewHash(existingBlobGitSHA1)
				if _, err := storer.EncodedObject(plumbing.BlobObject, h); err == nil {
					blobHash = h
					blobExists = true
				}
			}

			if !blobExists {
				frc, err := content.Read(fileEntry.Content, store, slotsClient)
				if err != nil {
					return plumbing.ZeroHash, fmt.Errorf("failed to read file %s content %s: %w", name, fileEntry.Content.Address, err)
				}
				blobData, err := io.ReadAll(frc)
				frc.Close()
				if err != nil {
					return plumbing.ZeroHash, fmt.Errorf("failed to read file %s bytes: %w", name, err)
				}

				encBlob := storer.NewEncodedObject()
				encBlob.SetType(plumbing.BlobObject)
				encBlob.SetSize(int64(len(blobData)))
				w, err := encBlob.Writer()
				if err != nil {
					return plumbing.ZeroHash, err
				}
				w.Write(blobData)
				w.Close()

				blobHash, err = storer.SetEncodedObject(encBlob)
				if err != nil {
					return plumbing.ZeroHash, fmt.Errorf("failed to write git blob: %w", err)
				}
				_ = kvIdx.RecordBlobMapping(ctx, blobHash.String(), fileEntry.Content.Address)
			}

			mode := filemode.Regular
			if fileEntry.Mode != nil && (strings.Contains(*fileEntry.Mode, "7") || strings.Contains(*fileEntry.Mode, "5")) {
				mode = filemode.Executable
			}

			treeEntries = append(treeEntries, object.TreeEntry{
				Name: name,
				Mode: mode,
				Hash: blobHash,
			})
		}
	}

	// Sort entries in standard git tree order
	sort.Slice(treeEntries, func(i, j int) bool {
		return treeEntries[i].Name < treeEntries[j].Name
	})

	gitTree := &object.Tree{
		Entries: treeEntries,
	}

	encTree := storer.NewEncodedObject()
	if err := gitTree.Encode(encTree); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to encode git tree: %w", err)
	}

	gitTreeHash, err := storer.SetEncodedObject(encTree)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to write git tree: %w", err)
	}

	_ = kvIdx.RecordTreeMapping(ctx, gitTreeHash.String(), treeLink.Address)
	return gitTreeHash, nil
}
