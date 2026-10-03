package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"invariant/internal/names"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

type createTestMockIDProvider struct {
	name string
}

func (m *createTestMockIDProvider) CurrentIdentity(ctx context.Context) (Identity, error) {
	return Identity{Name: m.name, Email: m.name + "@example.com"}, nil
}

func (m *createTestMockIDProvider) IdentityFromRemote(ctx context.Context, remoteAddr string) (*Identity, error) {
	id := Identity{Name: m.name, Email: m.name + "@example.com"}
	return &id, nil
}

func setupCreateTestServices(t *testing.T) (context.Context, storage.Storage, slots.Slots, names.Names, commit.Service) {
	t.Helper()
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("test-slots")
	namesClient := names.NewInMemoryNames()

	idProvider := &createTestMockIDProvider{name: "Alice"}
	SetDefaultIdentityProvider(idProvider)
	commitSvc := commit.NewLocalService(store, slotsClient, namesClient, idProvider)

	return ctx, store, slotsClient, namesClient, commitSvc
}

func TestCreateRepository_WritableByDefault(t *testing.T) {
	ctx, store, slotsClient, namesClient, commitSvc := setupCreateTestServices(t)
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	tmpDir := t.TempDir()
	repoName := "writablerepo"
	repoDir := filepath.Join(tmpDir, repoName)

	initTree := createTestTree(ctx, store, map[string]string{
		"README.md": "# Writable Repo\n",
	})

	// Create repository without specifying Writable or ReadOnly - should be writable by default
	_, _, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      repoName,
		TargetDir: repoDir,
		Content:   initTree,
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}

	mainWs := filepath.Join(repoDir, "main")
	meta, err := ReadWorkspaceMetadata(mainWs)
	if err != nil {
		t.Fatalf("ReadWorkspaceMetadata failed: %v", err)
	}
	if !meta.Writable {
		t.Errorf("Expected main workspace to be writable by default, got Writable=false")
	}

	// Verify we can commit directly to main
	testFile := filepath.Join(mainWs, "test.txt")
	if err := os.WriteFile(testFile, []byte("direct commit\n"), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	_, _, err = ExecuteCommit(ctx, store, slotsClient, commitSvc, CommitOptions{
		WorkspaceDir: mainWs,
		Messages:     []string{"Direct commit to main"},
	})
	if err != nil {
		t.Fatalf("ExecuteCommit on writable main failed: %v", err)
	}
}

func TestCreateRepository_ReadOnlyOption(t *testing.T) {
	ctx, store, slotsClient, namesClient, commitSvc := setupCreateTestServices(t)
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)

	tmpDir := t.TempDir()
	repoName := "readonlyrepo"
	repoDir := filepath.Join(tmpDir, repoName)

	initTree := createTestTree(ctx, store, map[string]string{
		"README.md": "# Read-Only Repo\n",
	})

	// Create repository with ReadOnly: true
	_, _, err := CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, CreateOptions{
		Name:      repoName,
		TargetDir: repoDir,
		Content:   initTree,
		ReadOnly:  true,
	})
	if err != nil {
		t.Fatalf("CreateRepository failed: %v", err)
	}

	mainWs := filepath.Join(repoDir, "main")
	meta, err := ReadWorkspaceMetadata(mainWs)
	if err != nil {
		t.Fatalf("ReadWorkspaceMetadata failed: %v", err)
	}
	if meta.Writable {
		t.Errorf("Expected main workspace to be read-only, got Writable=true")
	}

	// Verify that commit directly to main is rejected
	testFile := filepath.Join(mainWs, "test.txt")
	if err := os.WriteFile(testFile, []byte("direct commit\n"), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	_, _, err = ExecuteCommit(ctx, store, slotsClient, commitSvc, CommitOptions{
		WorkspaceDir: mainWs,
		Messages:     []string{"Direct commit to read-only main"},
	})
	if err == nil {
		t.Fatalf("Expected ExecuteCommit to fail on read-only workspace, but it succeeded")
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("Expected error to mention read-only, got: %v", err)
	}
}
