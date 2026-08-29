package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"invariant/internal/content"
	"invariant/internal/filetree"
	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

type workflowMockIDProvider struct {
	name string
}

func (m *workflowMockIDProvider) CurrentIdentity(ctx context.Context) (Identity, error) {
	return Identity{Name: m.name, Email: m.name + "@example.com"}, nil
}

func (m *workflowMockIDProvider) IdentityFromRemote(ctx context.Context, remoteAddr string) (*Identity, error) {
	id := Identity{Name: m.name, Email: m.name + "@example.com"}
	return &id, nil
}

func createTestTree(ctx context.Context, store storage.Storage, files map[string]string) string {
	var dir filetree.Directory
	for name, contentStr := range files {
		cLink, err := content.Write(bytes.NewReader([]byte(contentStr)), store, content.WriterOptions{})
		if err != nil {
			panic(err)
		}
		dir = append(dir, &filetree.FileEntry{
			BaseEntry: filetree.BaseEntry{
				Name: name,
				Kind: filetree.FileKind,
			},
			Content: cLink,
			Size:    uint64(len(contentStr)),
		})
	}
	dirData, _ := json.Marshal(dir)
	dirLink, err := content.Write(bytes.NewReader(dirData), store, content.WriterOptions{})
	if err != nil {
		panic(err)
	}
	return dirLink.Address
}

func TestWorkflow_EndToEnd(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("test-slots")
	namesClient := names.NewInMemoryNames()

	idProvider := &workflowMockIDProvider{name: "Alice"}
	SetDefaultIdentityProvider(idProvider)

	commitSvc := commit.NewLocalService(store, slotsClient, namesClient, idProvider)

	tempBase := t.TempDir()
	repoName := "myproject"
	repoDir := filepath.Join(tempBase, repoName)

	// 1. Create Repository
	cfg, rootCommitHash, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      repoName,
		TargetDir: repoDir,
		Writable:  false,
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}
	if cfg.DefaultBranch != "main" || rootCommitHash == "" {
		t.Fatalf("Unexpected repository config or root commit: %+v", cfg)
	}

	mainWorkspace := filepath.Join(repoDir, "main")
	if _, err := os.Stat(filepath.Join(mainWorkspace, ".invariant-workspace")); err != nil {
		t.Fatalf("Main workspace metadata not found: %v", err)
	}

	// 2. Create Change Branch
	meta, err := CreateChangeBranch(ctx, store, slotsClient, namesClient, commitSvc, ChangeOptions{
		RepoRoot:   repoDir,
		ChangeName: "feat-awesome",
		AuthorName: "Alice",
	})
	if err != nil {
		t.Fatalf("CreateChangeBranch failed: %v", err)
	}
	changeWorkspace := filepath.Join(repoDir, "feat-awesome")
	if meta.BranchName != ":Alice:myproject:feat-awesome" {
		t.Errorf("Unexpected change branch name: %s", meta.BranchName)
	}

	// 3. Edit files & generate temp file in workspace
	srcFile := filepath.Join(changeWorkspace, "feature.go")
	if err := os.WriteFile(srcFile, []byte("package main\n\nfunc Feature() string { return \"awesome\" }\n"), 0644); err != nil {
		t.Fatalf("Failed to write source file: %v", err)
	}
	tempFile := filepath.Join(changeWorkspace, "temp.log")
	if err := os.WriteFile(tempFile, []byte("temporary build cache"), 0644); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}

	// 4. Clean workspace
	cleaned, err := CleanWorkspace(ctx, store, slotsClient, commitSvc, CleanOptions{
		WorkspaceDir: changeWorkspace,
		Force:        true,
	})
	if err != nil {
		t.Fatalf("CleanWorkspace failed: %v", err)
	}
	if len(cleaned) != 2 {
		t.Logf("Cleaned files: %+v", cleaned)
	}

	// Re-write feature.go
	if err := os.WriteFile(srcFile, []byte("package main\n\nfunc Feature() string { return \"awesome\" }\n"), 0644); err != nil {
		t.Fatalf("Failed to rewrite source file: %v", err)
	}

	// 5. Get Status & Diff
	status, err := GetStatus(ctx, store, slotsClient, commitSvc, changeWorkspace)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if len(status.Entries) != 1 || status.Entries[0].Path != "feature.go" || status.Entries[0].Status != StatusAdded {
		t.Fatalf("Unexpected status entries: %+v", status.Entries)
	}

	diffStr, diffStat, err := GetDiff(ctx, store, slotsClient, commitSvc, DiffOptions{
		WorkspaceDir: changeWorkspace,
	})
	if err != nil {
		t.Fatalf("GetDiff failed: %v", err)
	}
	if diffStat.FilesChanged != 1 || diffStat.Insertions == 0 {
		t.Errorf("DiffStat unexpected: %+v", diffStat)
	}
	if len(diffStr) == 0 {
		t.Errorf("Expected non-empty diff string")
	}

	// 6. Execute Commit
	c1, h1, err := ExecuteCommit(ctx, store, slotsClient, commitSvc, CommitOptions{
		WorkspaceDir: changeWorkspace,
		Message:      "Implement awesome feature",
		AuthorName:   "Alice",
	})
	if err != nil {
		t.Fatalf("ExecuteCommit failed: %v", err)
	}
	if c1.Message != "Implement awesome feature" || h1 == "" {
		t.Errorf("Unexpected commit: %s, msg=%s", h1, c1.Message)
	}

	// 7. Execute Submit (clean fast-forward into main)
	submitResp, err := ExecuteSubmit(ctx, store, slotsClient, namesClient, commitSvc, SubmitOptions{
		WorkspaceDir: changeWorkspace,
		AuthorName:   "Alice",
	})
	if err != nil {
		t.Fatalf("ExecuteSubmit failed: %v", err)
	}
	if submitResp.NewHeadCommit != h1 {
		t.Errorf("Submit head commit mismatch: got %s, want %s", submitResp.NewHeadCommit, h1)
	}

	// Verify change directory was retired
	if _, err := os.Stat(changeWorkspace); !os.IsNotExist(err) {
		t.Errorf("Expected change workspace to be retired, but still exists")
	}
}

func TestWorkflow_SyncConflictResolution(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("test-slots")
	namesClient := names.NewInMemoryNames()

	idProvider := &workflowMockIDProvider{name: "Alice"}
	SetDefaultIdentityProvider(idProvider)

	commitSvc := commit.NewLocalService(store, slotsClient, namesClient, idProvider)

	tempBase := t.TempDir()
	repoName := "syncrepo"
	repoDir := filepath.Join(tempBase, repoName)

	initTree := createTestTree(ctx, store, map[string]string{
		"shared.txt": "Base content\n",
	})

	_, rootHash, _ := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      repoName,
		TargetDir: repoDir,
		Content:   initTree,
	})

	// Create change branch
	CreateChangeBranch(ctx, store, slotsClient, namesClient, commitSvc, ChangeOptions{
		RepoRoot:   repoDir,
		ChangeName: "feat-conflict",
		AuthorName: "Alice",
	})
	changeWorkspace := filepath.Join(repoDir, "feat-conflict")

	// Alice edits shared.txt and commits
	sharedPath := filepath.Join(changeWorkspace, "shared.txt")
	os.WriteFile(sharedPath, []byte("Alice modification\n"), 0644)
	ExecuteCommit(ctx, store, slotsClient, commitSvc, CommitOptions{
		WorkspaceDir: changeWorkspace,
		Message:      "Alice change",
		AuthorName:   "Alice",
	})

	// Concurrently Bob modifies shared.txt on main
	mainEntry, _ := namesClient.Get(ctx, repoName)
	bobTree := createTestTree(ctx, store, map[string]string{
		"shared.txt": "Bob conflicting modification\n",
	})
	_, bobHash, _ := commitSvc.CreateCommit(ctx, commit.CreateRequest{
		RepoName:   repoName,
		BranchName: "main",
		TreeLink:   content.ContentLink{Address: bobTree},
		Parents:    []string{rootHash},
		Message:    "Bob commit on main",
		Author:     Identity{Name: "Bob"},
	})
	slotsClient.Update(ctx, mainEntry.Value, bobHash, rootHash, nil)

	// 1. Sync detects conflict
	_, conflicts, err := ExecuteSync(ctx, store, slotsClient, namesClient, commitSvc, SyncOptions{
		WorkspaceDir: changeWorkspace,
	})
	if err != nil {
		t.Fatalf("ExecuteSync unexpected error: %v", err)
	}
	if len(conflicts) == 0 {
		t.Fatalf("Expected conflict on shared.txt, got none")
	}

	// 2. Resolve conflict
	os.WriteFile(sharedPath, []byte("Resolved content: Alice and Bob combined\n"), 0644)

	// 3. Continue sync
	rebasedHash, remaining, err := ExecuteSync(ctx, store, slotsClient, namesClient, commitSvc, SyncOptions{
		WorkspaceDir: changeWorkspace,
		Continue:     true,
	})
	if err != nil {
		t.Fatalf("ExecuteSync --continue failed: %v", err)
	}
	if len(remaining) > 0 || rebasedHash == "" {
		t.Fatalf("Unexpected remaining conflicts: %+v, rebasedHash: %s", remaining, rebasedHash)
	}
}
