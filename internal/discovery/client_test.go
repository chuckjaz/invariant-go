package discovery

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestClient(t *testing.T) {
	// Setup in-memory server
	server := NewDiscoveryServer(NewInMemoryDiscovery())
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Initialize client
	client := NewClient(ts.URL, ts.Client())

	// 1. Register
	reg := ServiceRegistration{
		ID:        "client-test-id",
		Address:   "http://client:8081",
		Protocols: []string{"client-protocol"},
		Tags:      []string{"cache", "source"},
	}

	err := client.Register(context.Background(), reg)
	if err != nil {
		t.Fatalf("Register error: %v", err)
	}

	// 2. Get
	desc, ok := client.Get(context.Background(), "client-test-id")
	if !ok {
		t.Fatal("Expected Get to return true")
	}
	if desc.Address != "http://client:8081" {
		t.Fatalf("Expected Address %s, got %s", "http://client:8081", desc.Address)
	}
	if len(desc.Tags) != 2 || desc.Tags[0] != "cache" || desc.Tags[1] != "source" {
		t.Fatalf("Expected tags [cache source], got %v", desc.Tags)
	}

	// 3. Find with matching protocol and no tag
	results, err := client.Find(context.Background(), "client-protocol", "", 1)
	if err != nil {
		t.Fatalf("Find error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}
	if len(results[0].Tags) != 2 || results[0].Tags[0] != "cache" || results[0].Tags[1] != "source" {
		t.Fatalf("Expected tags [cache source] in find result, got %v", results[0].Tags)
	}

	// 4. Find with matching tag and no protocol
	results, err = client.Find(context.Background(), "", "cache", 1)
	if err != nil {
		t.Fatalf("Find error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	// 5. Find with matching protocol and matching tag
	results, err = client.Find(context.Background(), "client-protocol", "cache", 1)
	if err != nil {
		t.Fatalf("Find error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	// 6. Find with matching protocol but mismatched tag
	results, err = client.Find(context.Background(), "client-protocol", "unknown-tag", 1)
	if err != nil {
		t.Fatalf("Find error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Expected 0 results, got %d", len(results))
	}

	// 7. Find with unknown protocol
	results, err = client.Find(context.Background(), "unknown-protocol", "", 1)
	if err != nil {
		t.Fatalf("Find error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Expected 0 results, got %d", len(results))
	}

	// 8. Test Non-existent Data
	_, ok = client.Get(context.Background(), "missing-id")
	if ok {
		t.Fatal("Expected Get to return false for non-existent service")
	}
}
