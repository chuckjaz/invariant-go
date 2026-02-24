package names_test

import (
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
	err := client.Put(name, value, tokens)
	if err != nil {
		t.Fatalf("Put error: %v", err)
	}

	// 2. Get
	entry, err := client.Get(name)
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
	err = client.Delete(name, "wrong-value")
	if err != names.ErrPreconditionFailed {
		t.Fatalf("expected ErrPreconditionFailed, got %v", err)
	}

	// 4. Delete correctly
	err = client.Delete(name, value)
	if err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	// 5. Get after delete
	_, err = client.Get(name)
	if err != names.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// 6. Delete already deleted
	err = client.Delete(name, value)
	if err != names.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
