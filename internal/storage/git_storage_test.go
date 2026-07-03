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

	// 2. Initialize GitStorage (using default cache capacity 0)
	gitStorage, err := NewGitStorage(repoDir, mockKVClient, 0)
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

func TestGitStorage_Caching(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	r, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("Failed to initialize git repository: %v", err)
	}

	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("Failed to get worktree: %v", err)
	}

	testContent := []byte("hello git storage caching test content")
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

	t.Run("default cache capacity (0) handles lookup and caches results", func(t *testing.T) {
		kvCallCount := 0
		mockKVClient := &mockKV{
			getFunc: func(ctx context.Context, txID *uint64, key string) ([]byte, uint64, error) {
				kvCallCount++
				expectedKey := "SHA256:" + sha256Hex
				if key == expectedKey {
					return []byte(gitSHA1Hex), 1, nil
				}
				return nil, 0, fmt.Errorf("key not found: %s", key)
			},
		}

		gitStorage, err := NewGitStorage(repoDir, mockKVClient, 0)
		if err != nil {
			t.Fatalf("Failed to create GitStorage: %v", err)
		}

		// First lookup - should hit KV client
		if !gitStorage.Has(ctx, sha256Hex) {
			t.Fatalf("Expected Has to return true")
		}
		if kvCallCount != 1 {
			t.Fatalf("Expected 1 KV call on first lookup, got %d", kvCallCount)
		}

		// Second lookup - should hit cache, NOT KV client
		if !gitStorage.Has(ctx, sha256Hex) {
			t.Fatalf("Expected Has to return true on second call")
		}
		if kvCallCount != 1 {
			t.Fatalf("Expected KV call count to remain 1 (cached), got %d", kvCallCount)
		}
	})

	t.Run("cache capacity is respected and evicts oldest items", func(t *testing.T) {
		kvCalls := make(map[string]int)
		mockKVClient := &mockKV{
			getFunc: func(ctx context.Context, txID *uint64, key string) ([]byte, uint64, error) {
				kvCalls[key]++
				return []byte(gitSHA1Hex), 1, nil
			},
		}

		// Create cache with capacity of 2
		gitStorage, err := NewGitStorage(repoDir, mockKVClient, 2)
		if err != nil {
			t.Fatalf("Failed to create GitStorage: %v", err)
		}

		// Query 1, 2 (will fill cache)
		gitStorage.Has(ctx, "addr1")
		gitStorage.Has(ctx, "addr2")

		// Verify they were called once
		if kvCalls["SHA256:addr1"] != 1 || kvCalls["SHA256:addr2"] != 1 {
			t.Fatalf("Expected addr1 and addr2 to have 1 KV call, got: %v", kvCalls)
		}

		// Query 1 again (moves to front of LRU)
		gitStorage.Has(ctx, "addr1")
		if kvCalls["SHA256:addr1"] != 1 {
			t.Fatalf("Expected addr1 to be cached, got calls: %d", kvCalls["SHA256:addr1"])
		}

		// Query 3 (exceeds capacity, evicts addr2 which is at the back of LRU)
		gitStorage.Has(ctx, "addr3")

		// Query 1 (should still be in cache)
		gitStorage.Has(ctx, "addr1")
		if kvCalls["SHA256:addr1"] != 1 {
			t.Fatalf("Expected addr1 to still be cached, got calls: %d", kvCalls["SHA256:addr1"])
		}

		// Query 2 (should have been evicted, causing a new KV call)
		gitStorage.Has(ctx, "addr2")
		if kvCalls["SHA256:addr2"] != 2 {
			t.Fatalf("Expected addr2 to be evicted and queried again, got calls: %d", kvCalls["SHA256:addr2"])
		}
	})

	t.Run("negative cache capacity disables caching", func(t *testing.T) {
		kvCallCount := 0
		mockKVClient := &mockKV{
			getFunc: func(ctx context.Context, txID *uint64, key string) ([]byte, uint64, error) {
				kvCallCount++
				return []byte(gitSHA1Hex), 1, nil
			},
		}

		// -1 disables caching
		gitStorage, err := NewGitStorage(repoDir, mockKVClient, -1)
		if err != nil {
			t.Fatalf("Failed to create GitStorage: %v", err)
		}

		gitStorage.Has(ctx, sha256Hex)
		gitStorage.Has(ctx, sha256Hex)

		if kvCallCount != 2 {
			t.Fatalf("Expected 2 KV calls (caching disabled), got %d", kvCallCount)
		}
	})
}
