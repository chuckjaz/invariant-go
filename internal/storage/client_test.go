package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http/httptest"
	"testing"
)

func TestClient(t *testing.T) {
	// Setup in-memory server
	server := NewStorageServer(NewInMemoryStorage())
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Initialize client
	client := NewClient(ts.URL, ts.Client())

	content := []byte("hello client test")
	hash := sha256.Sum256(content)
	expectedAddress := hex.EncodeToString(hash[:])

	// 1. Store
	address, err := client.Store(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store error: %v", err)
	}

	if address != expectedAddress {
		t.Fatalf("expected address %s, got %s", expectedAddress, address)
	}

	// 2. Has
	if !client.Has(expectedAddress) {
		t.Fatal("Expected Has to return true")
	}

	// 3. Size
	size, ok := client.Size(expectedAddress)
	if !ok || size != int64(len(content)) {
		t.Fatalf("Expected size %d, got %d (ok: %t)", len(content), size, ok)
	}

	// 4. Get
	r, ok := client.Get(expectedAddress)
	if !ok {
		t.Fatal("Expected Get to return true")
	}
	defer r.Close()

	readContent, _ := io.ReadAll(r)
	if string(readContent) != string(content) {
		t.Fatalf("Expected content %s, got %s", content, string(readContent))
	}

	// 5. StoreAt
	newContent := []byte("another payload entirely via client")
	hash2 := sha256.Sum256(newContent)
	newExpectedHash := hex.EncodeToString(hash2[:])

	// Incorrect store attempts
	success, err := client.StoreAt(newExpectedHash, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("StoreAt error: %v", err)
	}
	if success {
		t.Fatal("Expected StoreAt to fail natively when hash doesn't match content")
	}

	// Correct store attempts
	success, err = client.StoreAt(newExpectedHash, bytes.NewReader(newContent))
	if err != nil {
		t.Fatalf("StoreAt error: %v", err)
	}
	if !success {
		t.Fatal("Expected StoreAt to succeed")
	}

	if !client.Has(newExpectedHash) {
		t.Fatal("Expected Has to return true for StoreAt content")
	}

	// 6. Test Non-existent Data
	badHash := sha256.Sum256([]byte("missing"))
	badAddress := hex.EncodeToString(badHash[:])

	if client.Has(badAddress) {
		t.Fatal("Expected Has to return false for non-existent data")
	}

	_, ok = client.Size(badAddress)
	if ok {
		t.Fatal("Expected Size to return false for non-existent data")
	}

	_, ok = client.Get(badAddress)
	if ok {
		t.Fatal("Expected Get to return false for non-existent data")
	}
}
