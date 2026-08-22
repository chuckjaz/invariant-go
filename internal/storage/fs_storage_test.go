package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFileSystemStorage(t *testing.T) {
	// 1. Setup temporary directory for the test
	tmpDir := t.TempDir()
	fs := NewFileSystemStorage(tmpDir)

	content := []byte("hello file system test")
	hash1 := sha256.Sum256(content)
	expectedAddress := hex.EncodeToString(hash1[:])

	// 2. Store content
	address, err := fs.Store(context.Background(), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store error: %v", err)
	}

	if address != expectedAddress {
		t.Fatalf("expected address %s, got %s", expectedAddress, address)
	}

	expectedPath := filepath.Join(tmpDir, expectedAddress[0:2], expectedAddress[2:4], expectedAddress[4:])
	_, err = os.Stat(expectedPath)
	if os.IsNotExist(err) {
		t.Fatalf("Expected file at structured path %s does not exist", expectedPath)
	}

	// 4. Verify Has
	if !fs.Has(context.Background(), expectedAddress) {
		t.Fatal("Expected Has to return true")
	}

	// 5. Verify Size
	size, ok := fs.Size(context.Background(), expectedAddress)
	if !ok || size != int64(len(content)) {
		t.Fatalf("Expected size %d, got %d (ok: %t)", len(content), size, ok)
	}

	// 6. Verify Get
	r, ok := fs.Get(context.Background(), expectedAddress)
	if !ok {
		t.Fatal("Expected Get to return true")
	}
	defer r.Close()

	readContent, _ := io.ReadAll(r)
	if string(readContent) != string(content) {
		t.Fatalf("Expected content %s, got %s", content, string(readContent))
	}

	// 7. Verify StoreAt
	newContent := []byte("another payload entirely")
	hash2 := sha256.Sum256(newContent)
	newExpectedHash := hex.EncodeToString(hash2[:])

	// Incorrect store attempts
	success, err := fs.StoreAt(context.Background(), newExpectedHash, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("StoreAt error: %v", err)
	}
	if success {
		t.Fatal("Expected StoreAt to fail natively when hash doesn't match content")
	}

	// Correct store attempts
	success, err = fs.StoreAt(context.Background(), newExpectedHash, bytes.NewReader(newContent))
	if err != nil {
		t.Fatalf("StoreAt error: %v", err)
	}
	if !success {
		t.Fatal("Expected StoreAt to succeed")
	}

	newExpectedPath := filepath.Join(tmpDir, newExpectedHash[0:2], newExpectedHash[2:4], newExpectedHash[4:])
	_, err = os.Stat(newExpectedPath)
	if os.IsNotExist(err) {
		t.Fatalf("Expected file at structured path %s does not exist", newExpectedPath)
	}

	// 8. Verify List
	var list []string
	for chunk := range fs.List(context.Background(), 10) {
		list = append(list, chunk...)
	}

	if len(list) != 2 {
		t.Fatalf("Expected List to return 2 items, got %d", len(list))
	}

	found1 := false
	found2 := false
	for _, item := range list {
		if item == expectedAddress {
			found1 = true
		}
		if item == newExpectedHash {
			found2 = true
		}
	}

	if !found1 || !found2 {
		t.Fatalf("Expected List to contain both %s and %s, but got %v", expectedAddress, newExpectedHash, list)
	}
}

func TestFileSystemStorageExtra(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileSystemStorage(tmpDir)
	ctx := context.Background()

	if id := fs.ID(); len(id) != 64 {
		t.Errorf("Expected 64-character hex ID for file system storage, got %q (len=%d)", id, len(id))
	}

	subCh := fs.Subscribe(ctx)
	if subCh == nil {
		t.Fatal("Expected Subscribe channel to not be nil")
	}

	content := []byte("hello subscribe fs")
	addr, err := fs.Store(ctx, bytes.NewReader(content))
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
	removed, err := fs.Remove(ctx, addr)
	if err != nil || !removed {
		t.Errorf("Remove failed: err=%v, removed=%t", err, removed)
	}
	if fs.Has(ctx, addr) {
		t.Error("Expected address to be removed")
	}

	// Remove non-existent
	removed, err = fs.Remove(ctx, "non-existent")
	if err != nil || removed {
		t.Errorf("Expected Remove of non-existent to return false, nil; got removed=%t, err=%v", removed, err)
	}

	// Test BatchStore
	content1 := []byte("batch-block-fs-1")
	hash1 := sha256.Sum256(content1)
	addr1 := hex.EncodeToString(hash1[:])

	content2 := []byte("batch-block-fs-2")
	hash2 := sha256.Sum256(content2)
	addr2 := hex.EncodeToString(hash2[:])

	blocks := map[string]io.Reader{
		addr1: bytes.NewReader(content1),
		addr2: bytes.NewReader(content2),
	}
	err = fs.BatchStore(ctx, blocks)
	if err != nil {
		t.Fatalf("BatchStore failed: %v", err)
	}

	// Test BatchHas
	missing, err := fs.BatchHas(ctx, []string{addr1, addr2, "b3"})
	if err != nil {
		t.Fatalf("BatchHas failed: %v", err)
	}
	if len(missing) != 1 || missing[0] != "b3" {
		t.Errorf("Expected missing addresses to be ['b3'], got %v", missing)
	}
}

func TestFileSystemStorage_PathTraversalProtection(t *testing.T) {
	tmpDir := t.TempDir()
	fs := NewFileSystemStorage(tmpDir)
	ctx := context.Background()

	// Outside file creation to test unauthorized access
	secretFile := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secretFile, []byte("super secret"), 0644); err != nil {
		t.Fatal(err)
	}

	traversalAddresses := []string{
		"../../../../etc/passwd",
		"../secret.txt",
		"00/../../etc/shadow",
		"invalid_non_hex_address!",
		"",
	}

	for _, addr := range traversalAddresses {
		if fs.Has(ctx, addr) {
			t.Errorf("Has should return false for traversal address %q", addr)
		}
		if _, ok := fs.Get(ctx, addr); ok {
			t.Errorf("Get should return false for traversal address %q", addr)
		}
		if _, ok := fs.Size(ctx, addr); ok {
			t.Errorf("Size should return false for traversal address %q", addr)
		}
		if removed, err := fs.Remove(ctx, addr); removed || err != nil {
			t.Errorf("Remove should return false, nil for traversal address %q; got removed=%t, err=%v", addr, removed, err)
		}
		if success, _ := fs.StoreAt(ctx, addr, bytes.NewReader([]byte("data"))); success {
			t.Errorf("StoreAt should return false for traversal address %q", addr)
		}
	}
}
