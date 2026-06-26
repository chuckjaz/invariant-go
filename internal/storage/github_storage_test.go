package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type mockKV struct {
	getFunc func(ctx context.Context, txID *uint64, key string) ([]byte, uint64, error)
}

func (m *mockKV) Get(ctx context.Context, txID *uint64, key string) ([]byte, uint64, error) {
	return m.getFunc(ctx, txID, key)
}

func TestGitHubStorage(t *testing.T) {
	ctx := context.Background()

	// Content we will mock
	testContent := []byte("hello github storage unit tests")
	contentSHA256 := sha256.Sum256(testContent)
	contentSHA256Hex := hex.EncodeToString(contentSHA256[:])

	// Dummy 20-byte Git SHA1
	gitSHA1Bytes := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	gitSHA1Hex := hex.EncodeToString(gitSHA1Bytes)

	// Mock KV client in memory
	mockKVClient := &mockKV{
		getFunc: func(ctx context.Context, txID *uint64, key string) ([]byte, uint64, error) {
			expectedKey := "SHA256:" + string(contentSHA256[:])
			if key == expectedKey {
				return gitSHA1Bytes, 123, nil
			}
			return nil, 0, fmt.Errorf("key not found: %s", key)
		},
	}

	// Mock GitHub API server
	tsGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := fmt.Sprintf("/repos/testowner/testrepo/git/blobs/%s", gitSHA1Hex)
		if r.URL.Path != expectedPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Verify accept header
		accept := r.Header.Get("Accept")
		if accept != "application/vnd.github.v3.raw" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Verify optional token
		token := r.Header.Get("Authorization")
		if token != "Bearer testtoken" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if r.Method == "HEAD" {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(testContent)))
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method == "GET" {
			w.WriteHeader(http.StatusOK)
			w.Write(testContent)
			return
		}

		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer tsGitHub.Close()

	ghStorage := NewGitHubStorage("testowner", "testrepo", "testtoken", mockKVClient, nil)
	ghStorage.apiURL = tsGitHub.URL // Override API URL for testing

	// 1. Test ID
	if id := ghStorage.ID(); id != "github-testowner-testrepo" {
		t.Errorf("Expected ID %q, got %q", "github-testowner-testrepo", id)
	}

	// 2. Test Has (success)
	if !ghStorage.Has(ctx, contentSHA256Hex) {
		t.Errorf("Expected Has to return true for %s", contentSHA256Hex)
	}

	// 3. Test Has (not found in KV)
	dummyHex := hex.EncodeToString(make([]byte, 32))
	if ghStorage.Has(ctx, dummyHex) {
		t.Errorf("Expected Has to return false for unmapped address %s", dummyHex)
	}

	// 4. Test Get (success)
	rc, ok := ghStorage.Get(ctx, contentSHA256Hex)
	if !ok {
		t.Fatalf("Expected Get to succeed for %s", contentSHA256Hex)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("Failed to read body: %v", err)
	}
	if string(data) != string(testContent) {
		t.Errorf("Expected content %q, got %q", testContent, data)
	}

	// 5. Test Get (not found)
	_, ok = ghStorage.Get(ctx, dummyHex)
	if ok {
		t.Errorf("Expected Get to fail for unmapped address %s", dummyHex)
	}

	// 6. Test Size (success)
	size, ok := ghStorage.Size(ctx, contentSHA256Hex)
	if !ok {
		t.Fatalf("Expected Size to succeed for %s", contentSHA256Hex)
	}
	if size != int64(len(testContent)) {
		t.Errorf("Expected size %d, got %d", len(testContent), size)
	}

	// 7. Test Size (not found)
	_, ok = ghStorage.Size(ctx, dummyHex)
	if ok {
		t.Errorf("Expected Size to fail for unmapped address %s", dummyHex)
	}

	// 8. Test Store (error)
	_, err = ghStorage.Store(ctx, strings.NewReader("some data"))
	if err == nil {
		t.Error("Expected Store to return an error, got nil")
	}

	// 9. Test StoreAt (error)
	_, err = ghStorage.StoreAt(ctx, contentSHA256Hex, strings.NewReader("some data"))
	if err == nil {
		t.Error("Expected StoreAt to return an error, got nil")
	}
}
