package repository

import (
	"context"
	"testing"
	"time"

	"invariant/internal/content"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func TestCommitSerializationAndHashing(t *testing.T) {
	c1 := &Commit{
		Tree: content.ContentLink{
			Address: "d9e1c2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1",
		},
		Parents: []string{"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		Author: Identity{
			Name:  "Alice Dev",
			Email: "alice@example.com",
			Token: "ts-token-123",
		},
		Committer: Identity{
			Name:  "Alice Dev",
			Email: "alice@example.com",
		},
		Message:   "Initial feature commit",
		Timestamp: 1724930000,
		Tags: map[string]string{
			"review": "rev-999",
			"env":    "prod",
		},
		Refs: map[string]string{
			"supersedes": "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
		},
	}

	hash1, err := CalculateCommitHash(c1)
	if err != nil {
		t.Fatalf("CalculateCommitHash failed: %v", err)
	}
	if len(hash1) != 64 {
		t.Fatalf("Expected 64-char SHA256 hex hash, got %d chars: %s", len(hash1), hash1)
	}

	// Calculate again to verify determinism
	hash2, err := CalculateCommitHash(c1)
	if err != nil {
		t.Fatalf("CalculateCommitHash failed second time: %v", err)
	}
	if hash1 != hash2 {
		t.Fatalf("Commit hashing is non-deterministic: %s != %s", hash1, hash2)
	}
}

func TestWriteAndReadCommit(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("test-slots")

	originalCommit := &Commit{
		Tree: content.ContentLink{
			Address: "abc123def456abc123def456abc123def456abc123def456abc123def456abc1",
		},
		Parents: []string{},
		Author: Identity{
			Name:  "Bob Builder",
			Email: "bob@example.com",
		},
		Message:   "Create project foundation",
		Timestamp: time.Now().Unix(),
		Tags: map[string]string{
			"vcs": "ir",
		},
		Refs: map[string]string{},
	}

	commitHash, err := WriteCommit(ctx, store, originalCommit)
	if err != nil {
		t.Fatalf("WriteCommit failed: %v", err)
	}

	readBack, err := ReadCommit(ctx, store, slotsClient, commitHash)
	if err != nil {
		t.Fatalf("ReadCommit failed: %v", err)
	}

	if readBack.Message != originalCommit.Message {
		t.Errorf("Commit message mismatch: got %q, want %q", readBack.Message, originalCommit.Message)
	}
	if readBack.Author.Name != originalCommit.Author.Name {
		t.Errorf("Author name mismatch: got %q, want %q", readBack.Author.Name, originalCommit.Author.Name)
	}
	if readBack.Tree.Address != originalCommit.Tree.Address {
		t.Errorf("Tree address mismatch: got %q, want %q", readBack.Tree.Address, originalCommit.Tree.Address)
	}
	if readBack.Tags["vcs"] != "ir" {
		t.Errorf("Tag 'vcs' mismatch: got %q, want 'ir'", readBack.Tags["vcs"])
	}
}

func TestWriteAndReadRepositoryConfig(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("test-slots")

	cfg := &RepositoryConfig{
		DefaultBranch:  "main",
		MainSlotID:     "slot-main-12345",
		Encrypted:      true,
		Compressed:     true,
		WriteTag:       "engineering",
		ReviewRequired: true,
		Layers: []LayerDependency{
			{
				Repository: "proto-repo",
				Path:       "vendor/proto",
				Commit:     "commit-hash-abc",
			},
		},
		Settings: map[string]string{
			"merge.strategy": "fast-forward",
		},
		CreatedAt: 1724930000,
	}

	cfgHash, err := WriteRepositoryConfig(ctx, store, cfg)
	if err != nil {
		t.Fatalf("WriteRepositoryConfig failed: %v", err)
	}

	readCfg, err := ReadRepositoryConfig(ctx, store, slotsClient, cfgHash)
	if err != nil {
		t.Fatalf("ReadRepositoryConfig failed: %v", err)
	}

	if readCfg.DefaultBranch != "main" {
		t.Errorf("DefaultBranch mismatch: got %q, want 'main'", readCfg.DefaultBranch)
	}
	if readCfg.MainSlotID != "slot-main-12345" {
		t.Errorf("MainSlotID mismatch: got %q, want 'slot-main-12345'", readCfg.MainSlotID)
	}
	if !readCfg.ReviewRequired {
		t.Errorf("ReviewRequired mismatch: got false, want true")
	}
	if len(readCfg.Layers) != 1 || readCfg.Layers[0].Repository != "proto-repo" {
		t.Errorf("Layers mismatch: got %+v", readCfg.Layers)
	}
	if readCfg.Settings["merge.strategy"] != "fast-forward" {
		t.Errorf("Settings mismatch: got %q", readCfg.Settings["merge.strategy"])
	}
}

func TestCommitSortTagsAndRefs(t *testing.T) {
	c := &Commit{
		Tags: map[string]string{
			"zebra":  "z",
			"apple":  "a",
			"middle": "m",
		},
		Refs: map[string]string{
			"ref-b": "b",
			"ref-a": "a",
			"ref-c": "c",
		},
	}

	tags := SortTags(c.Tags)
	expectedTags := []string{"apple", "middle", "zebra"}
	for i, tag := range tags {
		if tag.Key != expectedTags[i] {
			t.Errorf("Tag order mismatch at %d: got %s, want %s", i, tag.Key, expectedTags[i])
		}
	}

	refs := SortRefs(c.Refs)
	expectedRefs := []string{"ref-a", "ref-b", "ref-c"}
	for i, ref := range refs {
		if ref.Name != expectedRefs[i] {
			t.Errorf("Ref order mismatch at %d: got %s, want %s", i, ref.Name, expectedRefs[i])
		}
	}
}
