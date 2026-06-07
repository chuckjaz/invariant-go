package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
)

func TestJoinedStorage_ReadWrites(t *testing.T) {
	ctx := context.Background()
	primary := NewInMemoryStorage()
	secondary := NewInMemoryStorage()
	joined := NewJoinedStorage(primary, secondary)

	// Pre-load secondary with a block (e.g. simulating default fallback)
	secondContent := []byte("secondary content")
	addr2, err := secondary.Store(ctx, bytes.NewReader(secondContent))
	if err != nil {
		t.Fatalf("failed setting up secondary: %v", err)
	}

	// 1. Reading fallback through joined
	if !joined.Has(ctx, addr2) {
		t.Errorf("expected JoinedStorage to Have %q (from secondary)", addr2)
	}
	rc, ok := joined.Get(ctx, addr2)
	if !ok {
		t.Fatalf("failed to Get from secondary via joined")
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(data, secondContent) {
		t.Errorf("expected %q, got %q", secondContent, data)
	}
	sz, ok := joined.Size(ctx, addr2)
	if !ok || sz != int64(len(secondContent)) {
		t.Errorf("expected size %d, got %d", len(secondContent), sz)
	}

	// 2. Writing to joined isolates to primary
	firstContent := []byte("primary content")
	addr1, err := joined.Store(ctx, bytes.NewReader(firstContent))
	if err != nil {
		t.Fatalf("failed to Store on joined: %v", err)
	}

	if !primary.Has(ctx, addr1) {
		t.Errorf("primary should have received the write for %q", addr1)
	}
	if secondary.Has(ctx, addr1) {
		t.Errorf("secondary should NOT have received the write for %q", addr1)
	}

	// 3. Size and Get resolving from primary correctly
	if !joined.Has(ctx, addr1) {
		t.Errorf("joined should have %q", addr1)
	}
	rc, ok = joined.Get(ctx, addr1)
	if !ok {
		t.Fatalf("failed to Get from primary via joined")
	}
	data, _ = io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(data, firstContent) {
		t.Errorf("expected %q, got %q", firstContent, data)
	}
	sz, ok = joined.Size(ctx, addr1)
	if !ok || sz != int64(len(firstContent)) {
		t.Errorf("expected size %d, got %d", len(firstContent), sz)
	}
}

// nonBatchStorage implements Storage but not BatchStorage
type nonBatchStorage struct {
	Storage
}

func TestJoinedStorageExtra(t *testing.T) {
	ctx := context.Background()
	primary := NewInMemoryStorage()
	secondary := NewInMemoryStorage()
	joined := NewJoinedStorage(primary, secondary)

	// Test StoreAt
	content := []byte("storeat joined content")
	addr, err := primary.Store(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("store failed: %v", err)
	}
	ok, err := joined.StoreAt(ctx, addr, bytes.NewReader(content))
	if err != nil || !ok {
		t.Fatalf("StoreAt failed: err=%v, ok=%t", err, ok)
	}

	// Test BatchStore with primary implementing BatchStorage
	content1 := []byte("joined-batch-1")
	hash1 := sha256.Sum256(content1)
	addr1 := hex.EncodeToString(hash1[:])

	content2 := []byte("joined-batch-2")
	hash2 := sha256.Sum256(content2)
	addr2 := hex.EncodeToString(hash2[:])

	blocks := map[string]io.Reader{
		addr1: bytes.NewReader(content1),
		addr2: bytes.NewReader(content2),
	}
	err = joined.BatchStore(ctx, blocks)
	if err != nil {
		t.Fatalf("BatchStore failed: %v", err)
	}

	// Test BatchHas with both primary and secondary implementing BatchStorage
	missing, err := joined.BatchHas(ctx, []string{addr1, addr2, "b3"})
	if err != nil {
		t.Fatalf("BatchHas failed: %v", err)
	}
	if len(missing) != 1 || missing[0] != "b3" {
		t.Errorf("Expected missing to be ['b3'], got %v", missing)
	}

	// Test BatchStore with primary NOT implementing BatchStorage
	nonBatchPrimary := &nonBatchStorage{primary}
	joinedNonBatch := NewJoinedStorage(nonBatchPrimary, secondary)

	content3 := []byte("joined-batch-4")
	hash3 := sha256.Sum256(content3)
	addr3 := hex.EncodeToString(hash3[:])

	blocks2 := map[string]io.Reader{
		addr3: bytes.NewReader(content3),
	}
	err = joinedNonBatch.BatchStore(ctx, blocks2)
	if err != nil {
		t.Fatalf("BatchStore (non-batch primary) failed: %v", err)
	}

	// Test BatchHas with primary/secondary NOT implementing BatchStorage
	nonBatchSecondary := &nonBatchStorage{secondary}
	joinedBothNonBatch := NewJoinedStorage(nonBatchPrimary, nonBatchSecondary)
	missing2, err := joinedBothNonBatch.BatchHas(ctx, []string{addr1, addr3, "b5"})
	if err != nil {
		t.Fatalf("BatchHas (non-batch primary/secondary) failed: %v", err)
	}
	if len(missing2) != 1 || missing2[0] != "b5" {
		t.Errorf("Expected missing to be ['b5'], got %v", missing2)
	}
}
