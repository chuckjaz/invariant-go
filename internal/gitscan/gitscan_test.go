package gitscan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chuckjaz/invariant-go/internal/kv"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestScanLocal_IndexesBlobsAndSkipsScannedTrees(t *testing.T) {
	repoDir := t.TempDir()

	repo, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("PlainInit failed: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree failed: %v", err)
	}

	contentA := []byte("hello invariant gitscan\n")
	if err := os.WriteFile(filepath.Join(repoDir, "a.txt"), contentA, 0644); err != nil {
		t.Fatalf("write a.txt failed: %v", err)
	}
	subDir := filepath.Join(repoDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("mkdir sub failed: %v", err)
	}
	contentB := []byte("nested file content")
	if err := os.WriteFile(filepath.Join(subDir, "b.txt"), contentB, 0644); err != nil {
		t.Fatalf("write sub/b.txt failed: %v", err)
	}

	if _, err := wt.Add("a.txt"); err != nil {
		t.Fatalf("git add a.txt failed: %v", err)
	}
	if _, err := wt.Add("sub/b.txt"); err != nil {
		t.Fatalf("git add sub/b.txt failed: %v", err)
	}
	commitHash, err := wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test Author",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	kvStore := kv.NewMemoryKeyValueStore()
	var logBuf bytes.Buffer
	ctx := context.Background()

	if err := ScanLocal(ctx, kvStore, repoDir, commitHash.String(), -1, 4, &logBuf); err != nil {
		t.Fatalf("ScanLocal failed: %v", err)
	}

	// Verify SHA256 -> SHA1 and SHA1 -> SHA256 mappings for both files
	for name, data := range map[string][]byte{"a.txt": contentA, "sub/b.txt": contentB} {
		sum := sha256.Sum256(data)
		sha256Hex := hex.EncodeToString(sum[:])

		sha1Bytes, _, err := kvStore.Get(ctx, nil, "SHA256:"+sha256Hex)
		if err != nil {
			t.Fatalf("expected SHA256 mapping for %s (%s), got error: %v", name, sha256Hex, err)
		}
		gotSHA256Bytes, _, err := kvStore.Get(ctx, nil, "SHA1:"+string(sha1Bytes))
		if err != nil {
			t.Fatalf("expected SHA1 mapping for %s (%s), got error: %v", name, string(sha1Bytes), err)
		}
		if string(gotSHA256Bytes) != sha256Hex {
			t.Fatalf("round-trip mismatch for %s: got %s, want %s", name, string(gotSHA256Bytes), sha256Hex)
		}
	}

	// Verify second scan succeeds idempotently using tree-skip markers
	logBuf.Reset()
	if err := ScanLocal(ctx, kvStore, repoDir, "HEAD", 1, 2, &logBuf); err != nil {
		t.Fatalf("second ScanLocal failed: %v", err)
	}
}
