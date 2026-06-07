package discovery

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFileSystemDiscovery(t *testing.T) {
	tmpDir := t.TempDir()

	fsd, err := NewFileSystemDiscovery(tmpDir, time.Millisecond*50)
	if err != nil {
		t.Fatalf("failed to create FileSystemDiscovery: %v", err)
	}

	ctx := context.Background()

	// Test Register
	reg1 := ServiceRegistration{
		ID:        "service1",
		Address:   "127.0.0.1:8080",
		Protocols: []string{"http", "grpc"},
	}
	if err := fsd.Register(ctx, reg1); err != nil {
		t.Fatalf("failed to register service1: %v", err)
	}

	// Test Get
	desc, ok := fsd.Get(ctx, "service1")
	if !ok {
		t.Fatalf("expected to find service1")
	}
	if desc.Address != reg1.Address {
		t.Errorf("expected address %s, got %s", reg1.Address, desc.Address)
	}
	if !reflect.DeepEqual(desc.Protocols, reg1.Protocols) {
		t.Errorf("expected protocols %v, got %v", reg1.Protocols, desc.Protocols)
	}

	// Test Find
	results, err := fsd.Find(ctx, "grpc", 10)
	if err != nil {
		t.Fatalf("Find failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "service1" {
		t.Errorf("expected service1, got %s", results[0].ID)
	}

	// Wait for snapshot
	time.Sleep(time.Millisecond * 100)
	fsd.Close()

	// Check if snapshot.json exists
	if _, err := os.Stat(filepath.Join(tmpDir, "snapshot.json")); os.IsNotExist(err) {
		t.Errorf("expected snapshot.json to be created")
	}

	// Reopen to test load from snapshot and journal
	fsd2, err := NewFileSystemDiscovery(tmpDir, time.Millisecond*50)
	if err != nil {
		t.Fatalf("failed to reopen FileSystemDiscovery: %v", err)
	}
	defer fsd2.Close()

	desc2, ok2 := fsd2.Get(ctx, "service1")
	if !ok2 {
		t.Fatalf("expected to find service1 after reopen")
	}
	if desc2.Address != reg1.Address {
		t.Errorf("expected address %s, got %s", reg1.Address, desc2.Address)
	}
}

func TestFileSystemDiscovery_HealthTracking(t *testing.T) {
	tmpDir := t.TempDir()
	fsd, err := NewFileSystemDiscovery(tmpDir, time.Hour)
	if err != nil {
		t.Fatalf("failed to create FileSystemDiscovery: %v", err)
	}
	defer fsd.Close()

	// Enable health tracking with a very short check interval and timeout
	fsd.WithHealthTracking(10*time.Millisecond, 20*time.Millisecond)
	if fsd.tracker == nil {
		t.Fatal("Expected tracker to be initialized")
	}

	// Re-enable health tracking (tests WithHealthTracking overriding existing tracker)
	fsd.WithHealthTracking(10*time.Millisecond, 20*time.Millisecond)

	ctx := context.Background()
	reg := ServiceRegistration{
		ID:        "healthy-srv",
		Address:   "http://localhost:9999",
		Protocols: []string{"http"},
	}
	if err := fsd.Register(ctx, reg); err != nil {
		t.Fatalf("failed to register: %v", err)
	}

	// Make sure it doesn't get removed immediately
	time.Sleep(5 * time.Millisecond)
	_, ok := fsd.Get(ctx, "healthy-srv")
	if !ok {
		t.Error("Expected service to still be registered")
	}
}
