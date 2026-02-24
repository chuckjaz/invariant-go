package names_test

import (
	"encoding/json"
	"invariant/internal/names"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNamesServer_PutAndGet(t *testing.T) {
	store := names.NewInMemoryNames()
	server := names.NewNamesServer(store)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// 1. PUT a name
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/my-name?value=abc&tokens=test-v1,storage-v1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %v", resp.StatusCode)
	}
	if resp.Header.Get("ETag") != "abc" {
		t.Errorf("expected ETag 'abc', got %v", resp.Header.Get("ETag"))
	}
	resp.Body.Close()

	// 2. GET the name
	resp, err = http.Get(ts.URL + "/my-name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %v", resp.StatusCode)
	}
	if resp.Header.Get("ETag") != "abc" {
		t.Errorf("expected ETag 'abc', got %v", resp.Header.Get("ETag"))
	}

	var entry names.NameEntry
	if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	resp.Body.Close()

	if entry.Value != "abc" {
		t.Errorf("expected value 'abc', got %v", entry.Value)
	}
	if len(entry.Tokens) != 2 || entry.Tokens[0] != "test-v1" || entry.Tokens[1] != "storage-v1" {
		t.Errorf("unexpected tokens %v", entry.Tokens)
	}
}

func TestNamesServer_Delete(t *testing.T) {
	store := names.NewInMemoryNames()
	server := names.NewNamesServer(store)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Add data directly to store
	store.Put("my-name", "abc", []string{"test-v1"})

	// 1. DELETE with wrong ETag
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/my-name", nil)
	req.Header.Set("If-Match", "wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("expected 412, got %v", resp.StatusCode)
	}
	resp.Body.Close()

	// 2. DELETE with correct ETag
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/my-name", nil)
	req.Header.Set("If-Match", "abc")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200/204, got %v", resp.StatusCode)
	}
	if resp.Header.Get("ETag") != "abc" {
		t.Errorf("expected ETag 'abc', got %v", resp.Header.Get("ETag"))
	}
	resp.Body.Close()

	// 3. GET should be 404
	resp, err = http.Get(ts.URL + "/my-name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %v", resp.StatusCode)
	}
	resp.Body.Close()
}
