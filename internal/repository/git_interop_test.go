package repository

import (
	"bytes"
	"context"
	"encoding/json"
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

	var progBuf bytes.Buffer

	// Import Git repo into Invariant workspace with progress tracking enabled
	res, err := ImportGitRepository(ctx, store, slotsClient, namesClient, commitSvc, kvClient, GitImportOptions{
		GitDir:             gitDir,
		Branch:             "master",
		TargetWorkspaceDir: mainWs,
		ShowProgress:       true,
		ProgressWriter:     &progBuf,
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

func TestGitImport_AlreadyImported(t *testing.T) {
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
	gitRepo, headGitHash := createTestGitRepo(t, gitDir)

	// 1. Create Invariant repository workspace
	repoName := "imported-repo-reuse"
	repoDir := filepath.Join(tempBase, repoName)
	CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      repoName,
		TargetDir: repoDir,
		Writable:  true,
	})

	mainWs := filepath.Join(repoDir, "main")

	// 2. First import
	res1, err := ImportGitRepository(ctx, store, slotsClient, namesClient, commitSvc, kvClient, GitImportOptions{
		GitDir:             gitDir,
		Branch:             "master",
		TargetWorkspaceDir: mainWs,
	})
	if err != nil {
		t.Fatalf("First ImportGitRepository failed: %v", err)
	}
	if res1.ImportedCommits != 2 {
		t.Fatalf("Expected 2 imported commits on first run, got %d", res1.ImportedCommits)
	}

	// Verify KV has commit and tree mappings
	kvIdx := NewGitKVIndex(kvClient)
	invCommit1, err := kvIdx.GetCommitInvariantHash(ctx, headGitHash.String())
	if err != nil || invCommit1 != res1.HeadCommit {
		t.Errorf("KV commit mapping missing: got %s, expected %s", invCommit1, res1.HeadCommit)
	}

	// 3. Second import on identical repository (should recognize commits as already imported)
	res2, err := ImportGitRepository(ctx, store, slotsClient, namesClient, commitSvc, kvClient, GitImportOptions{
		GitDir:             gitDir,
		Branch:             "master",
		TargetWorkspaceDir: mainWs,
	})
	if err != nil {
		t.Fatalf("Second ImportGitRepository failed: %v", err)
	}
	if res2.ImportedCommits != 0 {
		t.Errorf("Expected 0 new commits on re-import, got %d", res2.ImportedCommits)
	}
	if res2.HeadCommit != res1.HeadCommit {
		t.Errorf("HEAD commit changed on re-import: %s vs %s", res2.HeadCommit, res1.HeadCommit)
	}

	// 4. Add a third commit to Git repository
	wt, err := gitRepo.Worktree()
	if err != nil {
		t.Fatalf("failed to get git worktree: %v", err)
	}
	os.WriteFile(filepath.Join(gitDir, "src", "feature.go"), []byte("package main\n\nfunc Feature() {}\n"), 0644)
	wt.Add("src/feature.go")
	headGitHash3, err := wt.Commit("Add feature.go", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Git Author",
			Email: "author@example.com",
			When:  time.Unix(1700002000, 0),
		},
	})
	if err != nil {
		t.Fatalf("failed to commit 3 in git: %v", err)
	}

	// 5. Third import (incremental import)
	res3, err := ImportGitRepository(ctx, store, slotsClient, namesClient, commitSvc, kvClient, GitImportOptions{
		GitDir:             gitDir,
		Branch:             "master",
		TargetWorkspaceDir: mainWs,
	})
	if err != nil {
		t.Fatalf("Third ImportGitRepository failed: %v", err)
	}
	if res3.ImportedCommits != 1 {
		t.Errorf("Expected 1 newly imported commit, got %d", res3.ImportedCommits)
	}

	// Verify new commit parent is the previous Invariant HEAD
	head3CommitObj, err := commitSvc.GetCommit(ctx, res3.HeadCommit)
	if err != nil {
		t.Fatalf("Failed to retrieve 3rd commit: %v", err)
	}
	if len(head3CommitObj.Parents) != 1 || head3CommitObj.Parents[0] != res1.HeadCommit {
		t.Errorf("Expected parent %s for new commit, got %v", res1.HeadCommit, head3CommitObj.Parents)
	}

	// Verify 3rd commit mapping in KV
	invCommit3, err := kvIdx.GetCommitInvariantHash(ctx, headGitHash3.String())
	if err != nil || invCommit3 != res3.HeadCommit {
		t.Errorf("KV commit mapping for 3rd commit missing: got %s, expected %s", invCommit3, res3.HeadCommit)
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

func TestGitImportProgressTracker(t *testing.T) {
	tracker := &GitImportProgressTracker{}
	tracker.SetCommit(1, 5, "1234567890abcdef", "Feature commit message")

	var buf bytes.Buffer
	stop := tracker.Start(context.Background(), &buf)

	time.Sleep(100 * time.Millisecond)
	stop()

	if tracker.CurrentCommitIndex != 1 || tracker.TotalCommits != 5 {
		t.Errorf("Unexpected tracker commit index/total: %d/%d", tracker.CurrentCommitIndex, tracker.TotalCommits)
	}

	formatted := tracker.formatBytes(1024 * 1024 * 5)
	if !strings.Contains(formatted, "5.0 MB") {
		t.Errorf("formatBytes unexpected output: %s", formatted)
	}
}

func TestGitImport_CreateRepositoryOption(t *testing.T) {
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
	gitRepo, _ := createTestGitRepo(t, gitDir)

	repoName := "auto-created-from-git"
	targetRepoDir := filepath.Join(tempBase, repoName)

	// 1. Import and automatically create repository with initial branch 'main'
	res1, err := ImportGitRepository(ctx, store, slotsClient, namesClient, commitSvc, kvClient, GitImportOptions{
		GitDir:             gitDir,
		Branch:             "main",
		RepositoryName:     repoName,
		TargetWorkspaceDir: targetRepoDir,
		Writable:           true,
	})
	if err != nil {
		t.Fatalf("ImportGitRepository with RepositoryName failed: %v", err)
	}

	if !res1.CreatedRepo {
		t.Errorf("Expected CreatedRepo=true for new repository")
	}
	if res1.CreatedBranch != "main" {
		t.Errorf("Expected CreatedBranch %q, got %q", "main", res1.CreatedBranch)
	}
	if res1.HeadCommitLink.Address != res1.HeadCommit {
		t.Errorf("Expected HeadCommitLink address %q, got %q", res1.HeadCommit, res1.HeadCommitLink.Address)
	}

	// Verify workspace exists at targetRepoDir/main
	wsDir := filepath.Join(targetRepoDir, "main")
	meta, err := ReadWorkspaceMetadata(wsDir)
	if err != nil {
		t.Fatalf("Failed to read workspace metadata: %v", err)
	}
	if meta.CommitHash != res1.HeadCommit {
		t.Errorf("Expected workspace commit hash %s, got %s", res1.HeadCommit, meta.CommitHash)
	}
	if meta.RepoName != repoName {
		t.Errorf("Expected workspace repo name %s, got %s", repoName, meta.RepoName)
	}

	// Verify files are materialized in workspace
	readmeData, err := os.ReadFile(filepath.Join(wsDir, "README.md"))
	if err != nil || !strings.Contains(string(readmeData), "Updated documentation") {
		t.Errorf("README.md not properly materialized: %v", err)
	}

	// 2. Add a new branch 'feature' to the existing repository
	wt, _ := gitRepo.Worktree()
	os.WriteFile(filepath.Join(gitDir, "feature.txt"), []byte("feature content"), 0644)
	wt.Add("feature.txt")
	_, err = wt.Commit("Feature branch commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Git Author",
			Email: "author@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit feature in git: %v", err)
	}

	res2, err := ImportGitRepository(ctx, store, slotsClient, namesClient, commitSvc, kvClient, GitImportOptions{
		GitDir:             gitDir,
		Branch:             "feature",
		RepositoryName:     repoName,
		TargetWorkspaceDir: targetRepoDir,
		Writable:           true,
	})
	if err != nil {
		t.Fatalf("ImportGitRepository adding branch to existing repo failed: %v", err)
	}
	if res2.CreatedRepo {
		t.Errorf("Expected CreatedRepo=false when adding branch to existing repo")
	}
	if res2.CreatedBranch != "feature" {
		t.Errorf("Expected CreatedBranch %q, got %q", "feature", res2.CreatedBranch)
	}

	// Verify workspace exists at targetRepoDir/feature
	featureWsDir := filepath.Join(targetRepoDir, "feature")
	featureMeta, err := ReadWorkspaceMetadata(featureWsDir)
	if err != nil {
		t.Fatalf("Failed to read feature workspace metadata: %v", err)
	}
	if featureMeta.BranchName != "feature" {
		t.Errorf("Expected branch name 'feature', got %s", featureMeta.BranchName)
	}

	// 3. Attempt to re-import into the existing 'feature' branch (should error because branch already exists)
	_, err = ImportGitRepository(ctx, store, slotsClient, namesClient, commitSvc, kvClient, GitImportOptions{
		GitDir:             gitDir,
		Branch:             "feature",
		RepositoryName:     repoName,
		TargetWorkspaceDir: targetRepoDir,
		Writable:           true,
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("Expected error for already existing branch, got: %v", err)
	}
}

func TestCreateRepositoryFromCommitLink(t *testing.T) {
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
	_, _ = createTestGitRepo(t, gitDir)

	// 1. Import Git repo to CAS
	importRes, err := ImportGitRepository(ctx, store, slotsClient, namesClient, commitSvc, kvClient, GitImportOptions{
		GitDir: gitDir,
		Branch: "master",
	})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// 2. Create new repository directly from content link JSON
	linkJSON, _ := json.Marshal(importRes.HeadCommitLink)
	repoName := "repo-from-link"
	targetRepoDir := filepath.Join(tempBase, repoName)

	cfg, rootCommit, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      repoName,
		Content:   string(linkJSON),
		TargetDir: targetRepoDir,
		Writable:  true,
	})
	if err != nil {
		t.Fatalf("CreateRepository from commit content link JSON failed: %v", err)
	}

	if rootCommit != importRes.HeadCommit {
		t.Errorf("Expected root commit %s to match imported HEAD %s", rootCommit, importRes.HeadCommit)
	}
	if cfg.MainSlotID == "" {
		t.Fatalf("Expected non-empty MainSlotID")
	}

	// Verify slot address matches tip commit
	slotAddr, err := slotsClient.Get(ctx, cfg.MainSlotID)
	if err != nil || slotAddr != importRes.HeadCommit {
		t.Errorf("Slot address mismatch: got %s, expected %s", slotAddr, importRes.HeadCommit)
	}

	// Verify workspace files materialized
	wsDir := filepath.Join(targetRepoDir, "main")
	readmeData, err := os.ReadFile(filepath.Join(wsDir, "README.md"))
	if err != nil || !strings.Contains(string(readmeData), "Updated documentation") {
		t.Errorf("README.md not properly materialized in workspace: %v", err)
	}
}
