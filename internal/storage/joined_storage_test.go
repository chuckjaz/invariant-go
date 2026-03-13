package storage

import (
	"bytes"
	"context"
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
