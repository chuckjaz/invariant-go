package discovery

import (
	"context"
	"net/http/httptest"
	"testing"

	"invariant/internal/names"
)

func TestAdvertiseAndRegister(t *testing.T) {
	ctx := context.Background()
	disc := NewInMemoryDiscovery()

	// 1. empty advertise address with tags
	err := AdvertiseAndRegister(ctx, disc, "srv1", "", 8080, []string{"http"}, []string{"cache", "source"})
	if err != nil {
		t.Fatalf("AdvertiseAndRegister failed: %v", err)
	}

	desc, ok := disc.Get(ctx, "srv1")
	if !ok {
		t.Fatal("Expected srv1 to be registered")
	}
	if desc.Address != "http://localhost:8080" {
		t.Errorf("Expected address http://localhost:8080, got %q", desc.Address)
	}
	if len(desc.Tags) != 2 || desc.Tags[0] != "cache" || desc.Tags[1] != "source" {
		t.Errorf("Expected tags [cache source], got %v", desc.Tags)
	}

	// 2. advertise address without port
	err = AdvertiseAndRegister(ctx, disc, "srv2", "http://myhost", 9090, []string{"grpc"}, []string{"cache"})
	if err != nil {
		t.Fatalf("AdvertiseAndRegister failed: %v", err)
	}

	desc2, ok := disc.Get(ctx, "srv2")
	if !ok {
		t.Fatal("Expected srv2 to be registered")
	}
	if desc2.Address != "http://myhost:9090" {
		t.Errorf("Expected address http://myhost:9090, got %q", desc2.Address)
	}
	if len(desc2.Tags) != 1 || desc2.Tags[0] != "cache" {
		t.Errorf("Expected tags [cache], got %v", desc2.Tags)
	}

	// 3. invalid advertise address
	err = AdvertiseAndRegister(ctx, disc, "srv3", "http://:invalid", 9090, []string{"grpc"}, nil)
	if err == nil {
		t.Error("Expected error for invalid advertise address, got nil")
	}
}

func TestRegisterResolve(t *testing.T) {
	ctx := context.Background()
	disc := NewInMemoryDiscovery()

	// 1. Setup InMemoryNames server
	inmemNames := names.NewInMemoryNames()
	namesServer := names.NewNamesServer(inmemNames)
	ts := httptest.NewServer(namesServer)
	defer ts.Close()

	// 2. Register names service with discovery
	err := disc.Register(ctx, ServiceRegistration{
		ID:        inmemNames.ID(),
		Address:   ts.URL,
		Protocols: []string{"names-v1"},
	})
	if err != nil {
		t.Fatalf("Failed to register names service: %v", err)
	}

	// 3. Register a name
	err = RegisterName(ctx, disc, "my-service-alias", "srv-id-123", []string{"http"})
	if err != nil {
		t.Fatalf("RegisterName failed: %v", err)
	}

	// 4. Resolve name via ResolveName
	id, err := ResolveName(ctx, disc, "my-service-alias")
	if err != nil {
		t.Fatalf("ResolveName failed: %v", err)
	}
	if id != "srv-id-123" {
		t.Errorf("Expected ID srv-id-123, got %q", id)
	}

	// 5. Resolve name directly if length is 64
	id64 := "1234567890123456789012345678901234567890123456789012345678901234"
	idRes, err := ResolveName(ctx, disc, id64)
	if err != nil {
		t.Fatalf("ResolveName failed: %v", err)
	}
	if idRes != id64 {
		t.Errorf("Expected 64-char ID directly, got %q", idRes)
	}

	// 6. Resolve to service description using Resolve
	// First register the actual service in discovery
	err = disc.Register(ctx, ServiceRegistration{
		ID:        "srv-id-123",
		Address:   "http://localhost:9999",
		Protocols: []string{"http"},
	})
	if err != nil {
		t.Fatalf("Failed to register target service: %v", err)
	}

	desc, err := Resolve(ctx, disc, "my-service-alias")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if desc.ID != "srv-id-123" || desc.Address != "http://localhost:9999" {
		t.Errorf("Unexpected service description: %+v", desc)
	}

	// 7. Resolve failure cases
	_, err = Resolve(ctx, disc, "non-existent-alias")
	if err == nil {
		t.Error("Expected error resolving non-existent alias, got nil")
	}

	// Resolve direct ID not found
	id64NotFound := "9994567890123456789012345678901234567890123456789012345678901234"
	_, err = Resolve(ctx, disc, id64NotFound)
	if err == nil {
		t.Error("Expected error resolving non-existent 64-char ID, got nil")
	}

	// RegisterName failure (with nil discovery)
	err = RegisterName(ctx, nil, "alias", "id", nil)
	if err == nil {
		t.Error("Expected error for nil discovery service, got nil")
	}
}
