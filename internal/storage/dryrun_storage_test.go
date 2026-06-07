package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
)

func TestDryRunStorage(t *testing.T) {
	s := NewDryRunStorage().(*dryRunStorage)
	ctx := context.Background()

	if id := s.ID(); id != "dryrun-storage" {
		t.Errorf("Expected ID 'dryrun-storage', got %q", id)
	}

	content := []byte("test-data-for-dryrun")
	expectedHashBytes := sha256.Sum256(content)
	expectedAddress := hex.EncodeToString(expectedHashBytes[:])

	if s.Has(ctx, expectedAddress) {
		t.Error("Expected Has to return false for unseen key")
	}

	if rc, ok := s.Get(ctx, expectedAddress); ok || rc != nil {
		t.Error("Expected Get to return false and nil reader")
	}

	addr, err := s.Store(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	if addr != expectedAddress {
		t.Errorf("Expected address %q, got %q", expectedAddress, addr)
	}

	if !s.Has(ctx, expectedAddress) {
		t.Error("Expected Has to return true after Store")
	}

	success, err := s.StoreAt(ctx, "another-addr", bytes.NewReader(content))
	if err != nil || !success {
		t.Errorf("StoreAt failed: err=%v, success=%t", err, success)
	}
	if !s.Has(ctx, "another-addr") {
		t.Error("Expected Has to return true after StoreAt")
	}

	if size, ok := s.Size(ctx, expectedAddress); ok || size != 0 {
		t.Errorf("Expected Size to return 0 and false, got size=%d, ok=%t", size, ok)
	}

	// List should return closed channel immediately
	ch := s.List(ctx, 10)
	if ch == nil {
		t.Fatal("Expected List to return a channel")
	}
	_, open := <-ch
	if open {
		t.Error("Expected List channel to be closed immediately")
	}

	if removed, err := s.Remove(ctx, expectedAddress); err != nil || !removed {
		t.Errorf("Expected Remove to return true, nil; got removed=%t, err=%v", removed, err)
	}

	// Test BatchHas
	addresses := []string{expectedAddress, "non-existent"}
	missing, err := s.BatchHas(ctx, addresses)
	if err != nil {
		t.Fatalf("BatchHas failed: %v", err)
	}
	// expectedAddress was stored earlier, "non-existent" was not. So only "non-existent" is missing.
	if len(missing) != 1 || missing[0] != "non-existent" {
		t.Errorf("Expected BatchHas to return only missing addresses, got %v", missing)
	}

	// Test BatchStore
	blocks := map[string]io.Reader{
		"batch-addr-1": bytes.NewReader([]byte("batch-1")),
		"batch-addr-2": bytes.NewReader([]byte("batch-2")),
	}
	err = s.BatchStore(ctx, blocks)
	if err != nil {
		t.Errorf("BatchStore failed: %v", err)
	}
	if !s.Has(ctx, "batch-addr-1") || !s.Has(ctx, "batch-addr-2") {
		t.Error("Expected BatchStore keys to be marked as seen")
	}
}

func TestDryRunStorage_Error(t *testing.T) {
	s := NewDryRunStorage().(*dryRunStorage)
	ctx := context.Background()

	_, err := s.Store(ctx, errorReader{})
	if err != io.ErrUnexpectedEOF {
		t.Errorf("Expected ErrUnexpectedEOF, got %v", err)
	}
}
