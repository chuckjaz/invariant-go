package discovery

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceKey_InMemory(t *testing.T) {
	d := NewInMemoryDiscovery()
	ctx := context.Background()
	user := NodeIdentity{Name: "Alice", Email: "alice@example.com"}

	// 1. Create service key
	rec, err := d.CreateServiceKey(ctx, "worker-1", user, nil)
	if err != nil {
		t.Fatalf("failed to create service key: %v", err)
	}
	if rec.Key == "" || rec.ID == "" {
		t.Fatalf("invalid key or id: %+v", rec)
	}
	if rec.Name != "worker-1" || rec.CreatedBy.Email != "alice@example.com" {
		t.Fatalf("unexpected record: %+v", rec)
	}

	// 2. Verify key
	valid, verified, err := d.VerifyServiceKey(ctx, rec.Key)
	if err != nil || !valid || verified == nil {
		t.Fatalf("expected valid key, got valid=%v, err=%v", valid, err)
	}
	if verified.ID != rec.ID {
		t.Fatalf("expected id %s, got %s", rec.ID, verified.ID)
	}

	// 3. List keys
	keys, err := d.ListServiceKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d, err=%v", len(keys), err)
	}

	// 4. Revoke key
	if err := d.RevokeServiceKey(ctx, rec.ID, user); err != nil {
		t.Fatalf("failed to revoke key: %v", err)
	}

	// 5. Verify after revoke
	valid, _, err = d.VerifyServiceKey(ctx, rec.Key)
	if valid || err != ErrServiceKeyRevoked {
		t.Fatalf("expected ErrServiceKeyRevoked, got valid=%v, err=%v", valid, err)
	}
}

func TestServiceKey_FileSystem(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "fs-discovery-sk-test")
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)

	d, err := NewFileSystemDiscovery(dir, 0)
	if err != nil {
		t.Fatalf("failed to create fs discovery: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	user := NodeIdentity{Name: "Bob", Email: "bob@example.com"}

	// 1. Create
	rec, err := d.CreateServiceKey(ctx, "storage-node", user, nil)
	if err != nil {
		t.Fatalf("failed to create service key: %v", err)
	}

	// 2. Verify
	valid, verified, err := d.VerifyServiceKey(ctx, rec.Key)
	if err != nil || !valid || verified == nil {
		t.Fatalf("expected valid key, got valid=%v, err=%v", valid, err)
	}

	// 3. Close and reopen to verify durability
	d.Close()
	d2, err := NewFileSystemDiscovery(dir, 0)
	if err != nil {
		t.Fatalf("failed to reopen fs discovery: %v", err)
	}
	defer d2.Close()

	valid, verified2, err := d2.VerifyServiceKey(ctx, rec.Key)
	if err != nil || !valid || verified2 == nil {
		t.Fatalf("expected durable valid key, got valid=%v, err=%v", valid, err)
	}
	if verified2.Name != "storage-node" {
		t.Fatalf("expected name storage-node, got %s", verified2.Name)
	}
}

func TestServiceKey_HTTPAndPermissions(t *testing.T) {
	d := NewInMemoryDiscovery()
	server := NewDiscoveryServer(d)

	// Configure OAuth authenticator with a known user token
	userToken := "oauth-user-token-123"
	userIdentity := NodeIdentity{Name: "Alice Admin", Email: "alice@example.com"}
	server.WithOAuthAuthenticator(NewStaticAuthenticator(map[string]NodeIdentity{
		userToken: userIdentity,
	}))
	server.WithRequireAuth(true)

	ts := httptest.NewServer(server)
	defer ts.Close()

	ctx := context.Background()

	// 1. Anonymous request should be 401 Unauthorized
	anonClient := NewClient(ts.URL, ts.Client())
	_, err := anonClient.ListServiceKeys(ctx)
	if err == nil {
		t.Fatalf("expected 401 for anonymous client, got nil error")
	}

	// 2. User creates service key
	userClient := NewClient(ts.URL, ts.Client()).WithUserToken(userToken)
	createdKey, err := userClient.CreateServiceKey(ctx, "backup-worker", userIdentity, nil)
	if err != nil {
		t.Fatalf("user failed to create service key: %v", err)
	}
	if createdKey.Key == "" || createdKey.CreatedBy.Email != "alice@example.com" {
		t.Fatalf("unexpected created key: %+v", createdKey)
	}

	// 3. User lists service keys -> key should be masked in list
	keys, err := userClient.ListServiceKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("expected 1 listed key, got len=%d, err=%v", len(keys), err)
	}
	if keys[0].Key == createdKey.Key {
		t.Fatalf("expected key to be masked in list, got plaintext: %s", keys[0].Key)
	}

	// 4. Verify created key
	valid, verifiedRec, err := userClient.VerifyServiceKey(ctx, createdKey.Key)
	if err != nil || !valid || verifiedRec == nil {
		t.Fatalf("expected valid key verification, got valid=%v, err=%v", valid, err)
	}

	// 5. Worker uses the service key to register a service
	workerClient := NewClient(ts.URL, ts.Client()).WithServiceKey(createdKey.Key)
	err = workerClient.Register(ctx, ServiceRegistration{
		ID:        "worker-svc",
		Address:   "http://127.0.0.1:9090",
		Protocols: []string{"worker-v1"},
	})
	if err != nil {
		t.Fatalf("worker failed to register service with service key: %v", err)
	}

	// 6. Worker CANNOT create service keys (only users can!)
	_, err = workerClient.CreateServiceKey(ctx, "rogue-key", NodeIdentity{Name: "rogue"}, nil)
	if err == nil {
		t.Fatalf("expected service key to be forbidden from creating other service keys")
	}

	// 7. User revokes the service key
	err = userClient.RevokeServiceKey(ctx, createdKey.ID, userIdentity)
	if err != nil {
		t.Fatalf("user failed to revoke service key: %v", err)
	}

	// 8. Worker can no longer use the revoked service key
	err = workerClient.Register(ctx, ServiceRegistration{
		ID:        "worker-svc-2",
		Address:   "http://127.0.0.1:9091",
		Protocols: []string{"worker-v1"},
	})
	if err == nil {
		t.Fatalf("expected rejected request for revoked service key, got success")
	}
}
