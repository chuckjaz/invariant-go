package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

type changeTestMockIDProvider struct {
	name string
}

func (m *changeTestMockIDProvider) CurrentIdentity(ctx context.Context) (Identity, error) {
	return Identity{Name: m.name, Email: m.name + "@example.com"}, nil
}

func (m *changeTestMockIDProvider) IdentityFromRemote(ctx context.Context, remoteAddr string) (*Identity, error) {
	id := Identity{Name: m.name, Email: m.name + "@example.com"}
	return &id, nil
}

func setupChangeTestRepo(t *testing.T) (context.Context, storage.Storage, slots.Slots, names.Names, commit.Service, string, string) {
	t.Helper()
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("test-slots")
	namesClient := names.NewInMemoryNames()

	idProvider := &changeTestMockIDProvider{name: "Alice"}
	SetDefaultIdentityProvider(idProvider)
	commitSvc := commit.NewLocalService(store, slotsClient, namesClient, idProvider)

	tempBase := t.TempDir()
	repoName := "changerepo"
	repoDir := filepath.Join(tempBase, repoName)

	initTree := createTestTree(ctx, store, map[string]string{
		"README.md": "# Change Test Repo\n",
	})

	_, _, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      repoName,
		TargetDir: repoDir,
		Content:   initTree,
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}

	return ctx, store, slotsClient, namesClient, commitSvc, repoDir, repoName
}

func TestCreateChangeBranch_PeerDirectoryDefault(t *testing.T) {
	ctx, store, slotsClient, namesClient, commitSvc, repoDir, _ := setupChangeTestRepo(t)
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	mainWs := filepath.Join(repoDir, "main")

	// 1. Calling CreateChangeBranch from inside the branch workspace (mainWs)
	// By default (Subdirectory: false), it must create a peer directory of main: repoDir/feat-peer
	metaPeer, err := CreateChangeBranch(ctx, store, slotsClient, namesClient, commitSvc, ChangeOptions{
		RepoRoot:   mainWs,
		ChangeName: "feat-peer",
		AuthorName: "Alice",
	})
	if err != nil {
		t.Fatalf("CreateChangeBranch feat-peer failed: %v", err)
	}

	expectedPeerDir := filepath.Join(repoDir, "feat-peer")
	if metaPeer.WorkspaceDir != expectedPeerDir {
		t.Errorf("Expected peer workspace dir to be %s, got %s", expectedPeerDir, metaPeer.WorkspaceDir)
	}
	if filepath.Dir(metaPeer.WorkspaceDir) != repoDir {
		t.Errorf("Expected peer workspace parent to be %s, got %s", repoDir, filepath.Dir(metaPeer.WorkspaceDir))
	}
	if _, err := os.Stat(filepath.Join(expectedPeerDir, ".invariant-workspace")); err != nil {
		t.Errorf("Workspace metadata not found at %s: %v", expectedPeerDir, err)
	}

	// 2. Calling CreateChangeBranch from a nested sub-directory of mainWs
	nestedDir := filepath.Join(mainWs, "sub", "pkg")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatalf("Failed to create nested directory: %v", err)
	}
	metaNested, err := CreateChangeBranch(ctx, store, slotsClient, namesClient, commitSvc, ChangeOptions{
		RepoRoot:   nestedDir,
		ChangeName: "feat-nested",
		AuthorName: "Alice",
	})
	if err != nil {
		t.Fatalf("CreateChangeBranch feat-nested failed: %v", err)
	}

	expectedNestedPeerDir := filepath.Join(repoDir, "feat-nested")
	if metaNested.WorkspaceDir != expectedNestedPeerDir {
		t.Errorf("Expected nested workspace dir to be peer %s, got %s", expectedNestedPeerDir, metaNested.WorkspaceDir)
	}

	// 3. Calling CreateChangeBranch from the repository root (repoDir)
	metaFromRoot, err := CreateChangeBranch(ctx, store, slotsClient, namesClient, commitSvc, ChangeOptions{
		RepoRoot:   repoDir,
		ChangeName: "feat-from-root",
		AuthorName: "Alice",
	})
	if err != nil {
		t.Fatalf("CreateChangeBranch feat-from-root failed: %v", err)
	}
	expectedRootPeerDir := filepath.Join(repoDir, "feat-from-root")
	if metaFromRoot.WorkspaceDir != expectedRootPeerDir {
		t.Errorf("Expected root workspace dir to be %s, got %s", expectedRootPeerDir, metaFromRoot.WorkspaceDir)
	}
}

func TestCreateChangeBranch_SubdirectoryOption(t *testing.T) {
	ctx, store, slotsClient, namesClient, commitSvc, repoDir, _ := setupChangeTestRepo(t)
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	mainWs := filepath.Join(repoDir, "main")

	// When Subdirectory: true is explicitly specified, it should create a sub-directory of the branch
	metaSub, err := CreateChangeBranch(ctx, store, slotsClient, namesClient, commitSvc, ChangeOptions{
		RepoRoot:     mainWs,
		ChangeName:   "feat-sub",
		AuthorName:   "Alice",
		Subdirectory: true,
	})
	if err != nil {
		t.Fatalf("CreateChangeBranch feat-sub failed: %v", err)
	}

	expectedSubDir := filepath.Join(mainWs, "feat-sub")
	if metaSub.WorkspaceDir != expectedSubDir {
		t.Errorf("Expected sub-directory workspace dir to be %s, got %s", expectedSubDir, metaSub.WorkspaceDir)
	}
	if filepath.Dir(metaSub.WorkspaceDir) != mainWs {
		t.Errorf("Expected sub-directory parent to be %s, got %s", mainWs, filepath.Dir(metaSub.WorkspaceDir))
	}
	if _, err := os.Stat(filepath.Join(expectedSubDir, ".invariant-workspace")); err != nil {
		t.Errorf("Workspace metadata not found at %s: %v", expectedSubDir, err)
	}
}
