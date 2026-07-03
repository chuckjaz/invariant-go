package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
)

func TestInMemoryStorageList(t *testing.T) {
	mem := NewInMemoryStorage()

	// Initially empty
	var list []string
	for chunk := range mem.List(context.Background(), 10) {
		list = append(list, chunk...)
	}
	if len(list) != 0 {
		t.Fatalf("Expected empty list, got %d items", len(list))
	}

	addr1, err := mem.Store(context.Background(), bytes.NewReader([]byte("data1")))
	if err != nil {
		t.Fatalf("Store error: %v", err)
	}

	addr2, err := mem.Store(context.Background(), bytes.NewReader([]byte("data2")))
	if err != nil {
		t.Fatalf("Store error: %v", err)
	}

	for chunk := range mem.List(context.Background(), 10) {
		list = append(list, chunk...)
	}
	if len(list) != 2 {
		t.Fatalf("Expected list of size 2, got %d", len(list))
	}

	found1, found2 := false, false
	for _, a := range list {
		if a == addr1 {
			found1 = true
		} else if a == addr2 {
			found2 = true
		}
	}

	if !found1 || !found2 {
		t.Fatalf("List missing expected addresses: %v", list)
	}
}

func TestInMemoryStorageExtra(t *testing.T) {
	mem := NewInMemoryStorage()
	ctx := context.Background()

	subCh := mem.Subscribe(ctx)
	if subCh == nil {
		t.Fatal("Expected Subscribe channel to not be nil")
	}

	content := []byte("hello subscribe")
	addr, err := mem.Store(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store error: %v", err)
	}

	// Verify notification received
	select {
	case notifiedAddr := <-subCh:
		if notifiedAddr != addr {
			t.Errorf("Expected notification for %s, got %s", addr, notifiedAddr)
		}
	default:
		t.Error("Expected subscription notification, got none")
	}

	// Test Remove
	removed, err := mem.Remove(ctx, addr)
	if err != nil || !removed {
		t.Errorf("Remove failed: err=%v, removed=%t", err, removed)
	}
	if mem.Has(ctx, addr) {
		t.Error("Expected address to be removed")
	}

	// Remove non-existent
	removed, err = mem.Remove(ctx, "non-existent")
	if err != nil || removed {
		t.Errorf("Expected Remove of non-existent to return false, nil; got removed=%t, err=%v", removed, err)
	}

	// Test BatchStore
	content1 := []byte("batch-block-1")
	hash1 := sha256.Sum256(content1)
	addr1 := hex.EncodeToString(hash1[:])

	content2 := []byte("batch-block-2")
	hash2 := sha256.Sum256(content2)
	addr2 := hex.EncodeToString(hash2[:])

	blocks := map[string]io.Reader{
		addr1: bytes.NewReader(content1),
		addr2: bytes.NewReader(content2),
	}
	err = mem.BatchStore(ctx, blocks)
	if err != nil {
		t.Fatalf("BatchStore failed: %v", err)
	}

	// Test BatchHas
	missing, err := mem.BatchHas(ctx, []string{addr1, addr2, "b3"})
	if err != nil {
		t.Fatalf("BatchHas failed: %v", err)
	}
	if len(missing) != 1 || missing[0] != "b3" {
		t.Errorf("Expected missing addresses to be ['b3'], got %v", missing)
	}
}

func TestInMemoryStorage_Error(t *testing.T) {
	mem := NewInMemoryStorage()
	ctx := context.Background()

	// Test BatchStore failing due to wrong content hash (success = false)
	blocks := map[string]io.Reader{
		"invalid-hash": bytes.NewReader([]byte("wrong content")),
	}
	err := mem.BatchStore(ctx, blocks)
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled, got %v", err)
	}

	// Test BatchStore failing due to reader error (io.ErrUnexpectedEOF)
	blocksError := map[string]io.Reader{
		"any-hash": errorReader{},
	}
	err = mem.BatchStore(ctx, blocksError)
	if err != io.ErrUnexpectedEOF {
		t.Errorf("Expected io.ErrUnexpectedEOF, got %v", err)
	}
}
