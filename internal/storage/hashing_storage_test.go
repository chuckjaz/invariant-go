package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
)

func TestHashingStorage(t *testing.T) {
	s := NewHashingStorage().(*hashingStorage)
	ctx := context.Background()

	if id := s.ID(); id != "hashing-storage" {
		t.Errorf("Expected ID 'hashing-storage', got %q", id)
	}

	if s.Has(ctx, "any-address") {
		t.Error("Expected Has to always return false")
	}

	if rc, ok := s.Get(ctx, "any-address"); ok || rc != nil {
		t.Error("Expected Get to always return false and nil reader")
	}

	content := []byte("test-data-for-hashing")
	expectedHashBytes := sha256.Sum256(content)
	expectedAddress := hex.EncodeToString(expectedHashBytes[:])

	addr, err := s.Store(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	if addr != expectedAddress {
		t.Errorf("Expected address %q, got %q", expectedAddress, addr)
	}

	success, err := s.StoreAt(ctx, expectedAddress, bytes.NewReader(content))
	if err != nil || !success {
		t.Errorf("StoreAt failed: err=%v, success=%t", err, success)
	}

	if size, ok := s.Size(ctx, "any-address"); ok || size != 0 {
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

	if removed, err := s.Remove(ctx, "any-address"); err != nil || !removed {
		t.Errorf("Expected Remove to return true, nil; got removed=%t, err=%v", removed, err)
	}

	addresses := []string{"addr1", "addr2"}
	missing, err := s.BatchHas(ctx, addresses)
	if err != nil {
		t.Fatalf("BatchHas failed: %v", err)
	}
	if len(missing) != 2 || missing[0] != "addr1" || missing[1] != "addr2" {
		t.Errorf("Expected BatchHas to return all input addresses, got %v", missing)
	}

	blocks := map[string]io.Reader{
		expectedAddress: bytes.NewReader(content),
	}
	err = s.BatchStore(ctx, blocks)
	if err != nil {
		t.Errorf("BatchStore failed: %v", err)
	}
}

type errorReader struct{}

func (errorReader) Read(p []byte) (n int, err error) {
	return 0, io.ErrUnexpectedEOF
}

func TestHashingStorage_Error(t *testing.T) {
	s := NewHashingStorage().(*hashingStorage)
	ctx := context.Background()

	_, err := s.Store(ctx, errorReader{})
	if err != io.ErrUnexpectedEOF {
		t.Errorf("Expected ErrUnexpectedEOF, got %v", err)
	}

	_, err = s.StoreAt(ctx, "any-addr", errorReader{})
	if err != io.ErrUnexpectedEOF {
		t.Errorf("Expected ErrUnexpectedEOF, got %v", err)
	}

	blocks := map[string]io.Reader{
		"any-addr": errorReader{},
	}
	err = s.BatchStore(ctx, blocks)
	if err != io.ErrUnexpectedEOF {
		t.Errorf("Expected ErrUnexpectedEOF, got %v", err)
	}
}
