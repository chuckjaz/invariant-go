package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"
	"time"
)

func TestS3Storage(t *testing.T) {
	bucket := os.Getenv("TEST_S3_BUCKET")
	if bucket == "" {
		t.Skip("Skipping S3 storage test; set TEST_S3_BUCKET to run")
	}

	prefix := "test-invariant-s3/" + hex.EncodeToString([]byte(time.Now().String()))

	ctx := context.Background()
	s3s, err := NewS3Storage(ctx, bucket, prefix)
	if err != nil {
		t.Fatalf("Failed to initialize S3 storage: %v", err)
	}

	content := []byte("hello s3 storage test")
	hash1 := sha256.Sum256(content)
	expectedAddress := hex.EncodeToString(hash1[:])

	// test Store
	address, err := s3s.Store(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store error: %v", err)
	}
	if address != expectedAddress {
		t.Fatalf("expected address %s, got %s", expectedAddress, address)
	}

	// test Has
	if !s3s.Has(ctx, expectedAddress) {
		t.Fatal("Expected Has to return true")
	}

	// test Size
	size, ok := s3s.Size(ctx, expectedAddress)
	if !ok || size != int64(len(content)) {
		t.Fatalf("Expected size %d, got %d (ok: %t)", len(content), size, ok)
	}

	// test Get
	r, ok := s3s.Get(ctx, expectedAddress)
	if !ok {
		t.Fatal("Expected Get to return true")
	}
	defer r.Close()

	readContent, _ := io.ReadAll(r)
	if string(readContent) != string(content) {
		t.Fatalf("Expected content %s, got %s", content, string(readContent))
	}

	// test StoreAt
	newContent := []byte("another payload entirely for s3")
	hash2 := sha256.Sum256(newContent)
	newExpectedHash := hex.EncodeToString(hash2[:])

	// Incorrect store attempts
	success, err := s3s.StoreAt(ctx, newExpectedHash, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("StoreAt error: %v", err)
	}
	if success {
		t.Fatal("Expected StoreAt to fail inherently when hash doesn't match content")
	}

	// Correct store attempts
	success, err = s3s.StoreAt(ctx, newExpectedHash, bytes.NewReader(newContent))
	if err != nil {
		t.Fatalf("StoreAt error: %v", err)
	}
	if !success {
		t.Fatal("Expected StoreAt to succeed")
	}

	// test List
	var list []string
	for chunk := range s3s.List(ctx, 10) {
		list = append(list, chunk...)
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

	// test Remove
	success, err = s3s.Remove(ctx, expectedAddress)
	if err != nil {
		t.Fatalf("Remove error: %v", err)
	}
	if !success {
		t.Fatal("Expected Remove to return true")
	}

	if s3s.Has(ctx, expectedAddress) {
		t.Fatal("Expected Has to return false after removal")
	}

	s3s.Remove(ctx, newExpectedHash)
}
