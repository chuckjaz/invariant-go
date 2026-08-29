package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

type historyMockIDProvider struct {
	name string
}

func (m *historyMockIDProvider) CurrentIdentity(ctx context.Context) (Identity, error) {
	return Identity{Name: m.name, Email: m.name + "@example.com"}, nil
}

func (m *historyMockIDProvider) IdentityFromRemote(ctx context.Context, remoteAddr string) (*Identity, error) {
	id := Identity{Name: m.name, Email: m.name + "@example.com"}
	return &id, nil
}

func setupTestServices(t *testing.T) (storage.Storage, slots.Slots, names.Names, commit.Service) {
	t.Helper()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("test-slots")
	namesClient := names.NewInMemoryNames()
	idProvider := &historyMockIDProvider{name: "Alice"}
	SetDefaultIdentityProvider(idProvider)
	commitSvc := commit.NewLocalService(store, slotsClient, namesClient, idProvider)
	return store, slotsClient, namesClient, commitSvc
}

func TestLogAndShow(t *testing.T) {
	ctx := context.Background()
	store, slotsClient, namesClient, commitSvc := setupTestServices(t)

	tmpDir := t.TempDir()
	initSrc := filepath.Join(tmpDir, "src")
	os.MkdirAll(initSrc, 0755)
	os.WriteFile(filepath.Join(initSrc, "file1.txt"), []byte("Hello File 1\n"), 0644)
	os.WriteFile(filepath.Join(initSrc, "file2.txt"), []byte("Hello File 2\n"), 0644)

	cwd := filepath.Join(tmpDir, "work")
	os.MkdirAll(cwd, 0755)

	// Create repo
	_, _, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      "testlogrepo",
		Directory: initSrc,
		Writable:  true,
		TargetDir: filepath.Join(tmpDir, "testlogrepo"),
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}

	mainDir := filepath.Join(tmpDir, "testlogrepo", "main")

	// Commit 2: Modify file1
	os.WriteFile(filepath.Join(mainDir, "file1.txt"), []byte("Hello File 1 - Updated\n"), 0644)
	c2, h2, err := ExecuteCommit(ctx, store, slotsClient, commitSvc, CommitOptions{
		WorkspaceDir: mainDir,
		Messages:     []string{"Update file1"},
	})
	if err != nil {
		t.Fatalf("Commit 2 failed: %v", err)
	}
	_ = c2

	// Commit 3: Modify file2
	os.WriteFile(filepath.Join(mainDir, "file2.txt"), []byte("Hello File 2 - Updated\n"), 0644)
	c3, h3, err := ExecuteCommit(ctx, store, slotsClient, commitSvc, CommitOptions{
		WorkspaceDir: mainDir,
		Messages:     []string{"Update file2"},
	})
	if err != nil {
		t.Fatalf("Commit 3 failed: %v", err)
	}
	_ = c3

	// Test GetLog (all commits)
	entries, err := GetLog(ctx, store, slotsClient, commitSvc, LogOptions{
		WorkspaceDir: mainDir,
	})
	if err != nil {
		t.Fatalf("GetLog failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 log entries, got %d", len(entries))
	}

	logText := FormatLog(entries, true)
	if !strings.Contains(logText, "Update file2") || !strings.Contains(logText, "Update file1") {
		t.Errorf("expected formatted log to contain commit messages, got:\n%s", logText)
	}

	// Test GetLog with PathFilter ("file1.txt") -> Should only return Commit 2 and Root commit
	entriesF1, err := GetLog(ctx, store, slotsClient, commitSvc, LogOptions{
		WorkspaceDir: mainDir,
		PathFilter:   "file1.txt",
	})
	if err != nil {
		t.Fatalf("GetLog with path filter failed: %v", err)
	}
	if len(entriesF1) != 2 {
		t.Fatalf("expected 2 log entries for file1.txt, got %d", len(entriesF1))
	}

	// Test Show commit metadata & diff
	showCommitRes, err := GetShow(ctx, store, slotsClient, commitSvc, ShowOptions{
		WorkspaceDir: mainDir,
		Target:       h2,
	})
	if err != nil {
		t.Fatalf("GetShow commit failed: %v", err)
	}
	if showCommitRes.IsFileContent {
		t.Errorf("expected commit metadata text, got file content")
	}
	if !strings.Contains(showCommitRes.FormattedText, "Update file1") {
		t.Errorf("expected show to include message 'Update file1', got:\n%s", showCommitRes.FormattedText)
	}

	// Test Show file snapshot in CAS directly: "<commit>:<path>"
	showFileRes, err := GetShow(ctx, store, slotsClient, commitSvc, ShowOptions{
		WorkspaceDir: mainDir,
		Target:       h3 + ":file2.txt",
	})
	if err != nil {
		t.Fatalf("GetShow file snapshot failed: %v", err)
	}
	if !showFileRes.IsFileContent {
		t.Errorf("expected raw file content")
	}
	if string(showFileRes.FileContent) != "Hello File 2 - Updated\n" {
		t.Errorf("unexpected file content: %q", string(showFileRes.FileContent))
	}
}

func TestRestore(t *testing.T) {
	ctx := context.Background()
	store, slotsClient, namesClient, commitSvc := setupTestServices(t)

	tmpDir := t.TempDir()
	initSrc := filepath.Join(tmpDir, "src")
	os.MkdirAll(initSrc, 0755)
	os.WriteFile(filepath.Join(initSrc, "doc.txt"), []byte("Clean content\n"), 0644)

	_, _, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      "testrestorerepo",
		Directory: initSrc,
		Writable:  true,
		TargetDir: filepath.Join(tmpDir, "testrestorerepo"),
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}
	mainDir := filepath.Join(tmpDir, "testrestorerepo", "main")

	// Corrupt / modify doc.txt
	os.WriteFile(filepath.Join(mainDir, "doc.txt"), []byte("Dirty content\n"), 0644)

	// Call RestoreFiles
	restored, err := RestoreFiles(ctx, store, slotsClient, commitSvc, RestoreOptions{
		WorkspaceDir: mainDir,
		Path:         "doc.txt",
	})
	if err != nil {
		t.Fatalf("RestoreFiles failed: %v", err)
	}
	if len(restored) != 1 || restored[0] != "doc.txt" {
		t.Errorf("expected [doc.txt] restored, got %v", restored)
	}

	// Verify content on disk is restored
	data, _ := os.ReadFile(filepath.Join(mainDir, "doc.txt"))
	if string(data) != "Clean content\n" {
		t.Errorf("file was not restored to clean content: %q", string(data))
	}
}

func TestRevert(t *testing.T) {
	ctx := context.Background()
	store, slotsClient, namesClient, commitSvc := setupTestServices(t)

	tmpDir := t.TempDir()
	initSrc := filepath.Join(tmpDir, "src")
	os.MkdirAll(initSrc, 0755)
	os.WriteFile(filepath.Join(initSrc, "feature.txt"), []byte("v1 baseline\n"), 0644)

	_, _, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      "testrevertrepo",
		Directory: initSrc,
		Writable:  true,
		TargetDir: filepath.Join(tmpDir, "testrevertrepo"),
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}
	mainDir := filepath.Join(tmpDir, "testrevertrepo", "main")

	// Commit 2: Add bad feature
	os.WriteFile(filepath.Join(mainDir, "feature.txt"), []byte("v1 baseline\nbad feature\n"), 0644)
	_, h2, err := ExecuteCommit(ctx, store, slotsClient, commitSvc, CommitOptions{
		WorkspaceDir: mainDir,
		Messages:     []string{"Add bad feature"},
	})
	if err != nil {
		t.Fatalf("Commit 2 failed: %v", err)
	}

	// Commit 3: Add other file
	os.WriteFile(filepath.Join(mainDir, "other.txt"), []byte("other feature\n"), 0644)
	_, _, err = ExecuteCommit(ctx, store, slotsClient, commitSvc, CommitOptions{
		WorkspaceDir: mainDir,
		Messages:     []string{"Add other feature"},
	})
	if err != nil {
		t.Fatalf("Commit 3 failed: %v", err)
	}

	// Revert Commit 2
	revRes, err := ExecuteRevert(ctx, store, slotsClient, commitSvc, RevertOptions{
		WorkspaceDir: mainDir,
		CommitHash:   h2,
	})
	if err != nil {
		t.Fatalf("ExecuteRevert failed: %v", err)
	}

	if revRes.NewCommit.Refs["reverts"] != h2 {
		t.Errorf("expected revert ref to be %s, got %s", h2, revRes.NewCommit.Refs["reverts"])
	}

	// Check disk state: feature.txt should be back to "v1 baseline\n" and other.txt preserved
	fData, _ := os.ReadFile(filepath.Join(mainDir, "feature.txt"))
	if string(fData) != "v1 baseline\n" {
		t.Errorf("feature.txt not cleanly reverted, got: %q", string(fData))
	}
	oData, _ := os.ReadFile(filepath.Join(mainDir, "other.txt"))
	if string(oData) != "other feature\n" {
		t.Errorf("other.txt should be preserved, got: %q", string(oData))
	}
}

func TestBlameAndGrep(t *testing.T) {
	ctx := context.Background()
	store, slotsClient, namesClient, commitSvc := setupTestServices(t)

	tmpDir := t.TempDir()
	initSrc := filepath.Join(tmpDir, "src")
	os.MkdirAll(initSrc, 0755)
	os.WriteFile(filepath.Join(initSrc, "app.go"), []byte("package main\n\nfunc main() {}\n"), 0644)

	_, _, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      "testblamegrep",
		Directory: initSrc,
		Writable:  true,
		TargetDir: filepath.Join(tmpDir, "testblamegrep"),
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}
	mainDir := filepath.Join(tmpDir, "testblamegrep", "main")

	// Commit 2: Add helper
	os.WriteFile(filepath.Join(mainDir, "app.go"), []byte("package main\n\nfunc helper() {}\nfunc main() {}\n"), 0644)
	_, _, err = ExecuteCommit(ctx, store, slotsClient, commitSvc, CommitOptions{
		WorkspaceDir: mainDir,
		Messages:     []string{"Add helper function"},
	})
	if err != nil {
		t.Fatalf("Commit 2 failed: %v", err)
	}

	// Test Blame
	blameLines, err := GetBlame(ctx, store, slotsClient, commitSvc, BlameOptions{
		WorkspaceDir: mainDir,
		FilePath:     "app.go",
	})
	if err != nil {
		t.Fatalf("GetBlame failed: %v", err)
	}
	if len(blameLines) != 4 {
		t.Fatalf("expected 4 blame lines, got %d", len(blameLines))
	}
	blameText := FormatBlame(blameLines)
	if !strings.Contains(blameText, "func helper()") {
		t.Errorf("blame output missing helper, got:\n%s", blameText)
	}

	// Test Grep
	matches, err := GrepTree(ctx, store, slotsClient, commitSvc, GrepOptions{
		WorkspaceDir: mainDir,
		Pattern:      "helper",
	})
	if err != nil {
		t.Fatalf("GrepTree failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 grep match, got %d", len(matches))
	}
	if matches[0].LineNumber != 3 || !strings.Contains(matches[0].LineContent, "func helper()") {
		t.Errorf("unexpected grep match: %+v", matches[0])
	}
}

func TestStash(t *testing.T) {
	ctx := context.Background()
	store, slotsClient, namesClient, commitSvc := setupTestServices(t)

	tmpDir := t.TempDir()
	initSrc := filepath.Join(tmpDir, "src")
	os.MkdirAll(initSrc, 0755)
	os.WriteFile(filepath.Join(initSrc, "work.txt"), []byte("original\n"), 0644)

	_, _, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      "teststashrepo",
		Directory: initSrc,
		Writable:  true,
		TargetDir: filepath.Join(tmpDir, "teststashrepo"),
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}
	mainDir := filepath.Join(tmpDir, "teststashrepo", "main")

	// Edit file
	os.WriteFile(filepath.Join(mainDir, "work.txt"), []byte("work in progress\n"), 0644)

	// Stash push
	stashHash, err := StashPush(ctx, store, slotsClient, commitSvc, mainDir, "my stash test")
	if err != nil {
		t.Fatalf("StashPush failed: %v", err)
	}
	if stashHash == "" {
		t.Fatalf("expected non-empty stash hash")
	}

	// Verify working tree is clean
	data, _ := os.ReadFile(filepath.Join(mainDir, "work.txt"))
	if string(data) != "original\n" {
		t.Errorf("working tree was not cleaned after stash push, got: %q", string(data))
	}

	// Stash list
	entries, err := StashList(mainDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 stash entry, got %d (err: %v)", len(entries), err)
	}
	if !strings.Contains(entries[0].Message, "my stash test") {
		t.Errorf("unexpected stash message: %s", entries[0].Message)
	}

	// Stash pop
	popMsg, err := StashPop(ctx, store, slotsClient, commitSvc, mainDir)
	if err != nil {
		t.Fatalf("StashPop failed: %v", err)
	}
	if !strings.Contains(popMsg, "my stash test") {
		t.Errorf("unexpected pop message: %s", popMsg)
	}

	// Verify work in progress restored
	data, _ = os.ReadFile(filepath.Join(mainDir, "work.txt"))
	if string(data) != "work in progress\n" {
		t.Errorf("work in progress was not restored, got: %q", string(data))
	}

	// Verify stash stack is empty
	entries, err = StashList(mainDir)
	if len(entries) != 0 {
		t.Errorf("stash stack should be empty after pop, got %d", len(entries))
	}
}

func TestBisect(t *testing.T) {
	ctx := context.Background()
	store, slotsClient, namesClient, commitSvc := setupTestServices(t)

	tmpDir := t.TempDir()
	initSrc := filepath.Join(tmpDir, "src")
	os.MkdirAll(initSrc, 0755)
	os.WriteFile(filepath.Join(initSrc, "counter.txt"), []byte("1\n"), 0644)

	_, rootCommit, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      "testbisectrepo",
		Directory: initSrc,
		Writable:  true,
		TargetDir: filepath.Join(tmpDir, "testbisectrepo"),
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}
	mainDir := filepath.Join(tmpDir, "testbisectrepo", "main")

	// Create 4 more commits: C2, C3 (bug introduced), C4, C5
	var commitHashes []string
	commitHashes = append(commitHashes, rootCommit)

	for i := 2; i <= 5; i++ {
		content := fmt.Sprintf("%d\n", i)
		if i >= 3 {
			content = fmt.Sprintf("%d (BUG)\n", i)
		}
		os.WriteFile(filepath.Join(mainDir, "counter.txt"), []byte(content), 0644)
		_, h, err := ExecuteCommit(ctx, store, slotsClient, commitSvc, CommitOptions{
			WorkspaceDir: mainDir,
			Messages:     []string{fmt.Sprintf("Commit %d", i)},
		})
		if err != nil {
			t.Fatalf("Commit %d failed: %v", i, err)
		}
		commitHashes = append(commitHashes, h)
	}

	// Bug introduced at commitHashes[2] (Commit 3)
	badCommit := commitHashes[4]
	goodCommit := commitHashes[0]

	// Bisect start
	cand, _, err := BisectStart(ctx, store, slotsClient, commitSvc, mainDir, badCommit, goodCommit)
	if err != nil {
		t.Fatalf("BisectStart failed: %v", err)
	}
	if cand == "" {
		t.Fatalf("expected midpoint candidate")
	}

	// Check candidates until isolated
	isolatedCulprit := ""
	for range 5 {
		data, _ := os.ReadFile(filepath.Join(mainDir, "counter.txt"))
		isGood := !strings.Contains(string(data), "BUG")

		nextCand, _, found, err := BisectMark(ctx, store, slotsClient, commitSvc, mainDir, isGood, "")
		if err != nil {
			t.Fatalf("BisectMark step failed: %v", err)
		}
		if found {
			isolatedCulprit = nextCand
			break
		}
	}

	if isolatedCulprit != commitHashes[2] {
		t.Errorf("expected culprit %s (Commit 3), got %s", commitHashes[2], isolatedCulprit)
	}

	// Reset bisect
	if err := BisectReset(ctx, store, slotsClient, commitSvc, mainDir); err != nil {
		t.Fatalf("BisectReset failed: %v", err)
	}
}

func TestInteractiveRebaseAndCherryPick(t *testing.T) {
	ctx := context.Background()
	store, slotsClient, namesClient, commitSvc := setupTestServices(t)

	tmpDir := t.TempDir()
	initSrc := filepath.Join(tmpDir, "src")
	os.MkdirAll(initSrc, 0755)
	os.WriteFile(filepath.Join(initSrc, "main.txt"), []byte("main v1\n"), 0644)

	_, _, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      "testrebasecherry",
		Directory: initSrc,
		Writable:  true,
		TargetDir: filepath.Join(tmpDir, "testrebasecherry"),
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}
	mainDir := filepath.Join(tmpDir, "testrebasecherry", "main")

	// Create change branch
	meta, err := CreateChangeBranch(ctx, store, slotsClient, namesClient, commitSvc, ChangeOptions{
		RepoRoot:   mainDir,
		ChangeName: "feat-a",
	})
	if err != nil {
		t.Fatalf("CreateChangeBranch failed: %v", err)
	}

	// Commit 1 on feat-a
	os.WriteFile(filepath.Join(meta.WorkspaceDir, "feat.txt"), []byte("feat step 1\n"), 0644)
	_, h1, err := ExecuteCommit(ctx, store, slotsClient, commitSvc, CommitOptions{
		WorkspaceDir: meta.WorkspaceDir,
		Messages:     []string{"Feat step 1"},
	})
	if err != nil {
		t.Fatalf("feat commit 1 failed: %v", err)
	}

	// Commit 2 on feat-a
	os.WriteFile(filepath.Join(meta.WorkspaceDir, "feat.txt"), []byte("feat step 1 + 2\n"), 0644)
	_, h2, err := ExecuteCommit(ctx, store, slotsClient, commitSvc, CommitOptions{
		WorkspaceDir: meta.WorkspaceDir,
		Messages:     []string{"Feat step 2"},
	})
	if err != nil {
		t.Fatalf("feat commit 2 failed: %v", err)
	}

	// Test Interactive Rebase: Squash h2 into h1 with reworded message
	planText := fmt.Sprintf("pick %s Feat step 1\nsquash %s Feat step 1 + 2 completed\n", h1[:8], h2[:8])
	newHead, err := ExecuteInteractiveRebase(ctx, store, slotsClient, commitSvc, meta.WorkspaceDir, "main", planText)
	if err != nil {
		t.Fatalf("ExecuteInteractiveRebase failed: %v", err)
	}
	if newHead == "" {
		t.Fatalf("expected non-empty rebased head")
	}

	rebasedCommit, err := commitSvc.GetCommit(ctx, newHead)
	if err != nil {
		t.Fatalf("failed to get rebased commit: %v", err)
	}
	if !strings.Contains(rebasedCommit.Message, "Feat step 1") {
		t.Errorf("unexpected rebased commit message: %s", rebasedCommit.Message)
	}

	// Test Cherry-Pick: Cherry-pick the rebased feat commit into main branch
	created, err := ExecuteCherryPick(ctx, store, slotsClient, namesClient, commitSvc, CherryPickOptions{
		WorkspaceDir: mainDir,
		Target:       newHead,
	})
	if err != nil {
		t.Fatalf("ExecuteCherryPick failed: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("expected 1 cherry-picked commit, got %d", len(created))
	}

	// Check that feat.txt now exists in main
	featData, err := os.ReadFile(filepath.Join(mainDir, "feat.txt"))
	if err != nil || string(featData) != "feat step 1 + 2\n" {
		t.Errorf("cherry-picked content not found on main: %q (err: %v)", string(featData), err)
	}
}

func TestMountAndUnmount(t *testing.T) {
	ctx := context.Background()
	store, slotsClient, namesClient, commitSvc := setupTestServices(t)

	tmpDir := t.TempDir()
	initSrc := filepath.Join(tmpDir, "src")
	os.MkdirAll(initSrc, 0755)
	os.WriteFile(filepath.Join(initSrc, "readme.md"), []byte("# Mount Test\n"), 0644)

	_, _, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:       "mountablerepo",
		Directory:  initSrc,
		CreateOnly: true, // only in CAS
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}

	mountDir := filepath.Join(tmpDir, "mounted_repo")
	meta, err := MountRepository(ctx, store, slotsClient, namesClient, commitSvc, "mountablerepo", mountDir)
	if err != nil {
		t.Fatalf("MountRepository failed: %v", err)
	}
	if meta.BranchName != "main" {
		t.Errorf("expected branch main, got %s", meta.BranchName)
	}

	data, err := os.ReadFile(filepath.Join(mountDir, "main", "readme.md"))
	if err != nil || string(data) != "# Mount Test\n" {
		t.Errorf("mounted file content missing or incorrect: %q (err: %v)", string(data), err)
	}

	if err := UnmountRepository(filepath.Join(mountDir, "main")); err != nil {
		t.Fatalf("UnmountRepository failed: %v", err)
	}
}
