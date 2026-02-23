package storage

import (
	"bytes"
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
	address, err := fs.Store(bytes.NewReader(content))
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
	if !fs.Has(expectedAddress) {
		t.Fatal("Expected Has to return true")
	}

	// 5. Verify Size
	size, ok := fs.Size(expectedAddress)
	if !ok || size != int64(len(content)) {
		t.Fatalf("Expected size %d, got %d (ok: %t)", len(content), size, ok)
	}

	// 6. Verify Get
	r, ok := fs.Get(expectedAddress)
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
	success, err := fs.StoreAt(newExpectedHash, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("StoreAt error: %v", err)
	}
	if success {
		t.Fatal("Expected StoreAt to fail natively when hash doesn't match content")
	}

	// Correct store attempts
	success, err = fs.StoreAt(newExpectedHash, bytes.NewReader(newContent))
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
	for chunk := range fs.List(10) {
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
