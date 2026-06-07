package names_test

import (
	"context"
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
	store.Put(context.Background(), "my-name", "abc", []string{"test-v1"})

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

func TestNamesServer_IDAndLookupAndErrors(t *testing.T) {
	store := names.NewInMemoryNames()
	server := names.NewNamesServer(store)
	ts := httptest.NewServer(server) // tests ServeHTTP implementation directly as it implements http.Handler
	defer ts.Close()

	// 1. GET /id
	resp, err := http.Get(ts.URL + "/id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %v", resp.StatusCode)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/plain" {
		t.Errorf("expected Content-Type text/plain, got %v", resp.Header.Get("Content-Type"))
	}

	// 2. PUT a name and query it via GET /lookup/{id}
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/lookup-name?value=target-val", nil)
	respPut, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	respPut.Body.Close()

	respLookup, err := http.Get(ts.URL + "/lookup/target-val")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if respLookup.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %v", respLookup.StatusCode)
	}
	var lookupResults []string
	if err := json.NewDecoder(respLookup.Body).Decode(&lookupResults); err != nil {
		t.Fatalf("decode lookup results failed: %v", err)
	}
	respLookup.Body.Close()
	if len(lookupResults) != 1 || lookupResults[0] != "lookup-name" {
		t.Errorf("expected ['lookup-name'], got %v", lookupResults)
	}

	// 3. PUT with missing value parameter should return 400 Bad Request
	reqBadPut, _ := http.NewRequest(http.MethodPut, ts.URL+"/bad-name", nil)
	respBadPut, err := http.DefaultClient.Do(reqBadPut)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer respBadPut.Body.Close()
	if respBadPut.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for PUT missing value, got %v", respBadPut.StatusCode)
	}

	// 4. Test Not Implemented for non-identity Names backing store
	type nonIdentityNames struct {
		names.Names
	}
	serverNonId := names.NewNamesServer(&nonIdentityNames{Names: store})
	tsNonId := httptest.NewServer(serverNonId.Handler())
	defer tsNonId.Close()

	respNonId, err := http.Get(tsNonId.URL + "/id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer respNonId.Body.Close()
	if respNonId.StatusCode != http.StatusNotImplemented {
		t.Errorf("expected 501 Not Implemented, got %v", respNonId.StatusCode)
	}
}
