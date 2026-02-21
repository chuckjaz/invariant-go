package discovery

import (
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
	}

	err := client.Register(reg)
	if err != nil {
		t.Fatalf("Register error: %v", err)
	}

	// 2. Get
	desc, ok := client.Get("client-test-id")
	if !ok {
		t.Fatal("Expected Get to return true")
	}
	if desc.Address != "http://client:8081" {
		t.Fatalf("Expected Address %s, got %s", "http://client:8081", desc.Address)
	}

	// 3. Find with matching protocol
	results, err := client.Find("client-protocol", 1)
	if err != nil {
		t.Fatalf("Find error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	// 4. Find with unknown protocol
	results, err = client.Find("unknown-protocol", 1)
	if err != nil {
		t.Fatalf("Find error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Expected 0 results, got %d", len(results))
	}

	// 5. Test Non-existent Data
	_, ok = client.Get("missing-id")
	if ok {
		t.Fatal("Expected Get to return false for non-existent service")
	}
}
