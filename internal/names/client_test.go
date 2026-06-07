package names_test

import (
	"context"
	"invariant/internal/names"
	"net/http/httptest"
	"testing"
)

func TestClient(t *testing.T) {
	// Setup in-memory server
	server := names.NewNamesServer(names.NewInMemoryNames())
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Initialize client
	client := names.NewClient(ts.URL, ts.Client())

	name := "test-block"
	value := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tokens := []string{"block-v1"}

	// 1. Put
	err := client.Put(context.Background(), name, value, tokens)
	if err != nil {
		t.Fatalf("Put error: %v", err)
	}

	// 2. Get
	entry, err := client.Get(context.Background(), name)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if entry.Value != value {
		t.Fatalf("expected value %s, got %s", value, entry.Value)
	}
	if len(entry.Tokens) != 1 || entry.Tokens[0] != tokens[0] {
		t.Fatalf("expected tokens %v, got %v", tokens, entry.Tokens)
	}

	// 3. Delete with wrong precondition
	err = client.Delete(context.Background(), name, "wrong-value")
	if err != names.ErrPreconditionFailed {
		t.Fatalf("expected ErrPreconditionFailed, got %v", err)
	}

	// 4. Delete correctly
	err = client.Delete(context.Background(), name, value)
	if err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	// 5. Get after delete
	_, err = client.Get(context.Background(), name)
	if err != names.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// 6. Delete already deleted
	err = client.Delete(context.Background(), name, value)
	if err != names.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// 7. Put a new name and test Lookup
	err = client.Put(context.Background(), "lookup-name-1", "target-id", []string{"tok"})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	err = client.Put(context.Background(), "lookup-name-2", "target-id", []string{})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	results, err := client.Lookup(context.Background(), "target-id")
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 lookup results, got %v", results)
	}
	hasName1 := false
	hasName2 := false
	for _, n := range results {
		if n == "lookup-name-1" {
			hasName1 = true
		}
		if n == "lookup-name-2" {
			hasName2 = true
		}
	}
	if !hasName1 || !hasName2 {
		t.Errorf("expected names not found in lookup: %v", results)
	}
}

func TestClient_DefaultHTTPClient(t *testing.T) {
	client := names.NewClient("http://localhost:8080", nil)
	if client == nil {
		t.Error("expected NewClient to return non-nil Client when http.Client is nil")
	}
}
