package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"invariant/internal/kv"
	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func createTestGitRepo(t *testing.T, dir string) (*git.Repository, plumbing.Hash) {
	t.Helper()

	r, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// 1. First commit: README.md and main.go
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Sample Git Project\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644)

	wt.Add("README.md")
	wt.Add("src/main.go")

	_, err = wt.Commit("Initial git commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Git Author",
			Email: "author@example.com",
			When:  time.Unix(1700000000, 0),
		},
	})
	if err != nil {
		t.Fatalf("failed to commit 1: %v", err)
	}

	// 2. Second commit: add utils.go and update README
	os.WriteFile(filepath.Join(dir, "src", "utils.go"), []byte("package main\n\nfunc Helper() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Sample Git Project\nUpdated documentation.\n"), 0644)

	wt.Add("src/utils.go")
	wt.Add("README.md")

	c2, err := wt.Commit("Add utils and update readme", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Git Author",
			Email: "author@example.com",
			When:  time.Unix(1700001000, 0),
		},
	})
	if err != nil {
		t.Fatalf("failed to commit 2: %v", err)
	}

	return r, c2
}

func TestGitKVIndex(t *testing.T) {
	ctx := context.Background()
	kvStore := kv.NewMemoryKeyValueStore()
	idx := NewGitKVIndex(kvStore)

	// 1. Test Blob Mapping
	gitBlob := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	invBlob := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	if err := idx.RecordBlobMapping(ctx, gitBlob, invBlob); err != nil {
		t.Fatalf("RecordBlobMapping failed: %v", err)
	}

	resInv, err := idx.GetBlobInvariantSHA256(ctx, gitBlob)
	if err != nil || resInv != invBlob {
		t.Errorf("GetBlobInvariantSHA256: got %s, err %v", resInv, err)
	}

	resGit, err := idx.GetBlobGitSHA1(ctx, invBlob)
	if err != nil || resGit != gitBlob {
		t.Errorf("GetBlobGitSHA1: got %s, err %v", resGit, err)
	}

	// 2. Test Tree Mapping
	gitTree := "tree111122223333444455556666777788889999"
	invTree := "tree_inv_addr_00001111222233334444555566667777"
	if err := idx.RecordTreeMapping(ctx, gitTree, invTree); err != nil {
		t.Fatalf("RecordTreeMapping failed: %v", err)
	}

	treeAddr, err := idx.GetTreeInvariantAddress(ctx, gitTree)
	if err != nil || treeAddr != invTree {
		t.Errorf("GetTreeInvariantAddress: got %s, err %v", treeAddr, err)
	}

	// 3. Test Commit Mapping
	gitCommit := "commit111122223333444455556666777788889999"
	invCommit := "commit_inv_hash_00001111222233334444555566667777"
	if err := idx.RecordCommitMapping(ctx, gitCommit, invCommit); err != nil {
		t.Fatalf("RecordCommitMapping failed: %v", err)
	}

	commitHash, err := idx.GetCommitInvariantHash(ctx, gitCommit)
	if err != nil || commitHash != invCommit {
		t.Errorf("GetCommitInvariantHash: got %s, err %v", commitHash, err)
	}
}

func TestGitImport(t *testing.T) {
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("test-slots")
	namesClient := names.NewInMemoryNames()
	kvClient := kv.NewMemoryKeyValueStore()

	idProvider := &workflowMockIDProvider{name: "Alice"}
	SetDefaultIdentityProvider(idProvider)
	commitSvc := commit.NewLocalService(store, slotsClient, namesClient, idProvider)

	tempBase := t.TempDir()
	gitDir := filepath.Join(tempBase, "source-git")
	_, headGitHash := createTestGitRepo(t, gitDir)

	// Create Invariant repository workspace
	repoName := "imported-repo"
	repoDir := filepath.Join(tempBase, repoName)
	CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      repoName,
		TargetDir: repoDir,
		Writable:  true,
	})

	mainWs := filepath.Join(repoDir, "main")

	// Import Git repo into Invariant workspace
	res, err := ImportGitRepository(ctx, store, slotsClient, namesClient, commitSvc, kvClient, GitImportOptions{
		GitDir:             gitDir,
		Branch:             "master",
		TargetWorkspaceDir: mainWs,
	})
	if err != nil {
		t.Fatalf("ImportGitRepository failed: %v", err)
	}

	if res.ImportedCommits != 2 {
		t.Errorf("Expected 2 imported commits, got %d", res.ImportedCommits)
	}
	if res.HeadCommit == "" {
		t.Fatalf("Expected non-empty HeadCommit")
	}

	// Verify files were materialized in workspace
	readmeFile := filepath.Join(mainWs, "README.md")
	data, err := os.ReadFile(readmeFile)
	if err != nil || !strings.Contains(string(data), "Updated documentation") {
		t.Errorf("README.md not materialized correctly: %v, content=%s", err, string(data))
	}

	utilsFile := filepath.Join(mainWs, "src", "utils.go")
	if _, err := os.Stat(utilsFile); err != nil {
		t.Errorf("src/utils.go not found in workspace: %v", err)
	}

	// Verify commit metadata in CAS
	headCommitObj, err := commitSvc.GetCommit(ctx, res.HeadCommit)
	if err != nil {
		t.Fatalf("Failed to retrieve imported HEAD commit %s: %v", res.HeadCommit, err)
	}
	if headCommitObj.Author.Name != "Git Author" {
		t.Errorf("Expected author 'Git Author', got %s", headCommitObj.Author.Name)
	}
	if headCommitObj.Tags["git-commit"] != headGitHash.String() {
		t.Errorf("Expected tag git-commit %s, got %s", headGitHash.String(), headCommitObj.Tags["git-commit"])
	}
}

func TestGitExport(t *testing.T) {
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("test-slots")
	namesClient := names.NewInMemoryNames()
	kvClient := kv.NewMemoryKeyValueStore()

	idProvider := &workflowMockIDProvider{name: "Alice"}
	SetDefaultIdentityProvider(idProvider)
	commitSvc := commit.NewLocalService(store, slotsClient, namesClient, idProvider)

	tempBase := t.TempDir()
	repoName := "export-repo"
	repoDir := filepath.Join(tempBase, repoName)

	initTree := createTestTree(ctx, store, map[string]string{
		"config.json": `{"env": "production"}` + "\n",
		"lib/math.go": "package lib\n\nfunc Add(a, b int) int { return a + b }\n",
	})

	_, _, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      repoName,
		TargetDir: repoDir,
		Content:   initTree,
		Writable:  true,
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}

	mainWs := filepath.Join(repoDir, "main")

	// Make a second commit
	os.WriteFile(filepath.Join(mainWs, "lib", "math.go"), []byte("package lib\n\nfunc Add(a, b int) int { return a + b }\nfunc Sub(a, b int) int { return a - b }\n"), 0644)
	ExecuteCommit(ctx, store, slotsClient, commitSvc, CommitOptions{
		WorkspaceDir: mainWs,
		Message:      "Add Sub function in math.go",
		AuthorName:   "Alice",
	})

	// Export to target Git repo
	targetGitDir := filepath.Join(tempBase, "exported-git")
	res, err := ExportGitRepository(ctx, store, slotsClient, commitSvc, kvClient, GitExportOptions{
		WorkspaceDir: mainWs,
		TargetGitDir: targetGitDir,
		Branch:       "main",
	})
	if err != nil {
		t.Fatalf("ExportGitRepository failed: %v", err)
	}

	if res.ExportedCommits != 2 {
		t.Errorf("Expected 2 exported commits, got %d", res.ExportedCommits)
	}

	// Verify exported Git repository
	gitRepo, err := git.PlainOpen(targetGitDir)
	if err != nil {
		t.Fatalf("Failed to open exported git repo: %v", err)
	}

	headRef, err := gitRepo.Head()
	if err != nil {
		t.Fatalf("Exported git repo has no HEAD: %v", err)
	}

	if headRef.Hash().String() != res.GitHeadCommit {
		t.Errorf("HEAD hash mismatch: got %s, expected %s", headRef.Hash().String(), res.GitHeadCommit)
	}

	// Verify worktree files
	configFile := filepath.Join(targetGitDir, "config.json")
	if data, err := os.ReadFile(configFile); err != nil || !strings.Contains(string(data), "production") {
		t.Errorf("Exported config.json error: %v, content=%s", err, string(data))
	}

	mathFile := filepath.Join(targetGitDir, "lib", "math.go")
	if data, err := os.ReadFile(mathFile); err != nil || !strings.Contains(string(data), "Sub") {
		t.Errorf("Exported lib/math.go error: %v, content=%s", err, string(data))
	}
}

func TestGitRoundtrip(t *testing.T) {
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("test-slots")
	namesClient := names.NewInMemoryNames()
	kvClient := kv.NewMemoryKeyValueStore()

	idProvider := &workflowMockIDProvider{name: "Alice"}
	SetDefaultIdentityProvider(idProvider)
	commitSvc := commit.NewLocalService(store, slotsClient, namesClient, idProvider)

	tempBase := t.TempDir()
	sourceGitDir := filepath.Join(tempBase, "original-git")
	createTestGitRepo(t, sourceGitDir)

	// 1. Create Invariant repository workspace
	repoName := "roundtrip-repo"
	repoDir := filepath.Join(tempBase, repoName)
	_, _, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      repoName,
		TargetDir: repoDir,
		Writable:  true,
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}

	mainWs := filepath.Join(repoDir, "main")

	// 2. Import Git -> Invariant
	importRes, err := ImportGitRepository(ctx, store, slotsClient, namesClient, commitSvc, kvClient, GitImportOptions{
		GitDir:             sourceGitDir,
		Branch:             "master",
		TargetWorkspaceDir: mainWs,
	})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// 3. Export Invariant -> Target Git
	targetGitDir := filepath.Join(tempBase, "roundtrip-git")
	exportRes, err := ExportGitRepository(ctx, store, slotsClient, commitSvc, kvClient, GitExportOptions{
		WorkspaceDir: mainWs,
		TargetGitDir: targetGitDir,
		Branch:       "master",
	})
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if importRes.ImportedCommits != exportRes.ExportedCommits {
		t.Errorf("Commit count mismatch: imported %d vs exported %d", importRes.ImportedCommits, exportRes.ExportedCommits)
	}

	// 4. Verify file content equality between original and roundtrip Git repositories
	origReadme, _ := os.ReadFile(filepath.Join(sourceGitDir, "README.md"))
	exportedReadme, _ := os.ReadFile(filepath.Join(targetGitDir, "README.md"))
	if string(origReadme) != string(exportedReadme) {
		t.Errorf("README.md content mismatch between original and exported Git repos:\nOriginal: %s\nExported: %s", origReadme, exportedReadme)
	}

	origUtils, _ := os.ReadFile(filepath.Join(sourceGitDir, "src", "utils.go"))
	exportedUtils, _ := os.ReadFile(filepath.Join(targetGitDir, "src", "utils.go"))
	if string(origUtils) != string(exportedUtils) {
		t.Errorf("src/utils.go content mismatch:\nOriginal: %s\nExported: %s", origUtils, exportedUtils)
	}
}
