package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestGitStorage(t *testing.T) {
	ctx := context.Background()

	// 1. Create a temporary local git repository
	repoDir := t.TempDir()
	r, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("Failed to initialize git repository: %v", err)
	}

	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("Failed to get worktree: %v", err)
	}

	testContent := []byte("hello git storage test content")
	testPath := filepath.Join(repoDir, "test.txt")
	if err := os.WriteFile(testPath, testContent, 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	_, err = wt.Add("test.txt")
	if err != nil {
		t.Fatalf("Failed to add test.txt: %v", err)
	}

	commitHash, err := wt.Commit("Add test.txt", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Bot",
			Email: "bot@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	commitObj, err := r.CommitObject(commitHash)
	if err != nil {
		t.Fatalf("Failed to get commit: %v", err)
	}
	tree, err := commitObj.Tree()
	if err != nil {
		t.Fatalf("Failed to get tree: %v", err)
	}
	file, err := tree.File("test.txt")
	if err != nil {
		t.Fatalf("Failed to get file test.txt: %v", err)
	}

	gitSHA1Hex := file.Hash.String()
	hasher := sha256.New()
	hasher.Write(testContent)
	sha256Hex := hex.EncodeToString(hasher.Sum(nil))

	// Mock KV client
	mockKVClient := &mockKV{
		getFunc: func(ctx context.Context, txID *uint64, key string) ([]byte, uint64, error) {
			expectedKey := "SHA256:" + sha256Hex
			if key == expectedKey {
				return []byte(gitSHA1Hex), 1, nil
			}
			return nil, 0, fmt.Errorf("key not found: %s", key)
		},
	}

	// 2. Initialize GitStorage
	gitStorage, err := NewGitStorage(repoDir, mockKVClient)
	if err != nil {
		t.Fatalf("Failed to create GitStorage: %v", err)
	}

	// 3. Test ID
	expectedID := fmt.Sprintf("git-%s", repoDir)
	if id := gitStorage.ID(); id != expectedID {
		t.Errorf("Expected ID %q, got %q", expectedID, id)
	}

	// 4. Test Has
	if !gitStorage.Has(ctx, sha256Hex) {
		t.Errorf("Expected Has to return true for %s", sha256Hex)
	}

	dummyHex := hex.EncodeToString(make([]byte, 32))
	if gitStorage.Has(ctx, dummyHex) {
		t.Errorf("Expected Has to return false for unmapped address %s", dummyHex)
	}

	// 5. Test Get
	rc, ok := gitStorage.Get(ctx, sha256Hex)
	if !ok {
		t.Fatalf("Expected Get to succeed for %s", sha256Hex)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("Failed to read body: %v", err)
	}
	if string(data) != string(testContent) {
		t.Errorf("Expected content %q, got %q", testContent, data)
	}

	_, ok = gitStorage.Get(ctx, dummyHex)
	if ok {
		t.Errorf("Expected Get to fail for unmapped address %s", dummyHex)
	}

	// 6. Test Size
	size, ok := gitStorage.Size(ctx, sha256Hex)
	if !ok {
		t.Fatalf("Expected Size to succeed for %s", sha256Hex)
	}
	if size != int64(len(testContent)) {
		t.Errorf("Expected size %d, got %d", len(testContent), size)
	}

	_, ok = gitStorage.Size(ctx, dummyHex)
	if ok {
		t.Errorf("Expected Size to fail for unmapped address %s", dummyHex)
	}

	// 7. Test Store (error)
	_, err = gitStorage.Store(ctx, strings.NewReader("some data"))
	if err == nil {
		t.Error("Expected Store to return an error, got nil")
	}

	// 8. Test StoreAt (error)
	_, err = gitStorage.StoreAt(ctx, sha256Hex, strings.NewReader("some data"))
	if err == nil {
		t.Error("Expected StoreAt to return an error, got nil")
	}
}
