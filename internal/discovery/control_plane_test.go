package discovery

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func TestInMemoryDiscovery_ControlPlane(t *testing.T) {
	d := NewInMemoryDiscovery()
	ctx := context.Background()

	// Initially no keys
	keys, err := d.AuthorizedKeys(ctx)
	if err != nil {
		t.Fatalf("AuthorizedKeys failed: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected 0 authorized keys, got %v", keys)
	}

	// Register client
	clientPubKey := "nodekey:client111"
	clientID := NodeIdentity{Name: "alice", Email: "alice@example.com"}
	if err := d.RegisterClient(ctx, clientPubKey, clientID); err != nil {
		t.Fatalf("RegisterClient failed: %v", err)
	}

	// Verify client key
	ok, id, err := d.VerifyKey(ctx, clientPubKey)
	if err != nil {
		t.Fatalf("VerifyKey failed: %v", err)
	}
	if !ok || id == nil || id.Name != "alice" || id.Email != "alice@example.com" {
		t.Fatalf("VerifyKey returned unexpected result: ok=%v, id=%v", ok, id)
	}

	// Register a service with public key
	svcPubKey := "nodekey:service222"
	svcReg := ServiceRegistration{
		ID:        "storage1",
		Address:   "itp://teststorage",
		Protocols: []string{"storage-v1"},
		PublicKey: svcPubKey,
		Identity:  &NodeIdentity{Name: "storage-node-1"},
	}
	if err := d.Register(ctx, svcReg); err != nil {
		t.Fatalf("Register service failed: %v", err)
	}

	// Verify service key
	ok, id, err = d.VerifyKey(ctx, svcPubKey)
	if err != nil {
		t.Fatalf("VerifyKey service failed: %v", err)
	}
	if !ok || id == nil || id.Name != "storage-node-1" {
		t.Fatalf("VerifyKey service returned unexpected result: ok=%v, id=%v", ok, id)
	}

	// Verify unknown key
	ok, id, err = d.VerifyKey(ctx, "nodekey:unknown")
	if err != nil {
		t.Fatalf("VerifyKey unknown failed: %v", err)
	}
	if ok || id != nil {
		t.Fatalf("expected unknown key to not be authorized, got ok=%v, id=%v", ok, id)
	}

	// List authorized keys
	keys, err = d.AuthorizedKeys(ctx)
	if err != nil {
		t.Fatalf("AuthorizedKeys failed: %v", err)
	}
	if len(keys) != 2 || !slices.Contains(keys, clientPubKey) || !slices.Contains(keys, svcPubKey) {
		t.Fatalf("unexpected authorized keys: %v", keys)
	}
}

func TestFileSystemDiscovery_ControlPlane(t *testing.T) {
	tmpDir := t.TempDir()
	fsd, err := NewFileSystemDiscovery(tmpDir, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewFileSystemDiscovery failed: %v", err)
	}
	defer fsd.Close()

	ctx := context.Background()

	clientPubKey := "nodekey:client-fs"
	clientID := NodeIdentity{Name: "bob", Email: "bob@example.com"}
	if err := fsd.RegisterClient(ctx, clientPubKey, clientID); err != nil {
		t.Fatalf("RegisterClient failed: %v", err)
	}

	ok, id, err := fsd.VerifyKey(ctx, clientPubKey)
	if err != nil {
		t.Fatalf("VerifyKey failed: %v", err)
	}
	if !ok || id == nil || id.Name != "bob" {
		t.Fatalf("unexpected verify result: ok=%v, id=%v", ok, id)
	}

	// Snapshot and reopen
	if err := fsd.Snapshot(); err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	fsd.Close()

	fsd2, err := NewFileSystemDiscovery(tmpDir, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("reopening FileSystemDiscovery failed: %v", err)
	}
	defer fsd2.Close()

	ok, id, err = fsd2.VerifyKey(ctx, clientPubKey)
	if err != nil {
		t.Fatalf("VerifyKey after reopen failed: %v", err)
	}
	if !ok || id == nil || id.Name != "bob" {
		t.Fatalf("unexpected verify result after reopen: ok=%v, id=%v", ok, id)
	}
}

func TestDiscoveryServer_ControlPlaneHTTP(t *testing.T) {
	d := NewInMemoryDiscovery()
	serviceKey := "test-secret-token"
	server := NewDiscoveryServer(d).WithServiceKeys(serviceKey)

	ts := httptest.NewServer(server)
	defer ts.Close()

	ctx := context.Background()

	// Client with wrong token fails
	badClient := NewControlPlaneClient(ts.URL, ts.Client(), "wrong-token")
	err := badClient.RegisterClient(ctx, "nodekey:foo", NodeIdentity{Name: "foo"})
	if err == nil {
		t.Fatal("expected unauthorized error with wrong token, got nil")
	}

	// Client with correct token succeeds
	goodClient := NewControlPlaneClient(ts.URL, ts.Client(), serviceKey)
	clientKey := "nodekey:testclient"
	err = goodClient.RegisterClient(ctx, clientKey, NodeIdentity{Name: "charlie", Email: "charlie@example.com"})
	if err != nil {
		t.Fatalf("RegisterClient failed: %v", err)
	}

	// Verify key
	ok, id, err := goodClient.VerifyKey(ctx, clientKey)
	if err != nil {
		t.Fatalf("VerifyKey failed: %v", err)
	}
	if !ok || id == nil || id.Name != "charlie" {
		t.Fatalf("unexpected verify key response: ok=%v, id=%v", ok, id)
	}

	// Authorized keys
	keys, err := goodClient.AuthorizedKeys(ctx)
	if err != nil {
		t.Fatalf("AuthorizedKeys failed: %v", err)
	}
	if len(keys) != 1 || keys[0] != clientKey {
		t.Fatalf("unexpected keys: %v", keys)
	}

	// Test unified discovery.Client
	discClient := NewClient(ts.URL, ts.Client()).WithServiceKey(serviceKey)
	ok, id, err = discClient.VerifyKey(ctx, clientKey)
	if err != nil {
		t.Fatalf("discClient.VerifyKey failed: %v", err)
	}
	if !ok || id == nil || id.Name != "charlie" {
		t.Fatalf("unexpected verify result from discClient: ok=%v, id=%v", ok, id)
	}
}
