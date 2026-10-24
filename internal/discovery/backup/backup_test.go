package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chuckjaz/invariant-go/internal/discovery"
	"github.com/chuckjaz/invariant-go/internal/slots"
	"github.com/chuckjaz/invariant-go/internal/storage"
)

func TestChainedBackup_BackupAndReplay(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "discovery-backup-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	memStorage := storage.NewInMemoryStorage()
	memSlots := slots.NewMemorySlots("mem-slots")
	slotID := "discovery-slot"

	// 1. Initialize ChainedBackup
	b, err := NewChainedBackup(BackupOptions{
		BaseDir:    tempDir,
		Storage:    memStorage,
		Slots:      memSlots,
		SlotID:     slotID,
		MaxEntries: 10,
	})
	if err != nil {
		t.Fatalf("failed to create ChainedBackup: %v", err)
	}

	// 2. Initialize FileSystemDiscovery wired to ChainedBackup
	fsDir := filepath.Join(tempDir, "fs-data")
	d, err := discovery.NewFileSystemDiscovery(fsDir, 0)
	if err != nil {
		t.Fatalf("failed to create fs discovery: %v", err)
	}
	d.WithEventSink(b)

	ctx := context.Background()

	// 3. Perform discovery mutations:
	// A. Register services
	err = d.Register(ctx, discovery.ServiceRegistration{
		ID:        "storage-1",
		Address:   "http://10.0.0.1:8080",
		Protocols: []string{"storage-v1"},
		Tags:      []string{"originals"},
	})
	if err != nil {
		t.Fatalf("failed to register service: %v", err)
	}

	err = d.Register(ctx, discovery.ServiceRegistration{
		ID:        "kv-1",
		Address:   "http://10.0.0.2:8080",
		Protocols: []string{"kv-v1"},
	})
	if err != nil {
		t.Fatalf("failed to register service: %v", err)
	}

	// B. Register client
	clientPubKey := "wg-client-public-key-xyz"
	err = d.RegisterClient(ctx, clientPubKey, discovery.NodeIdentity{
		Name:  "Alice",
		Email: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("failed to register client: %v", err)
	}

	// C. Create service keys
	creator := discovery.NodeIdentity{Name: "Admin", Email: "admin@example.com"}
	sk1, err := d.CreateServiceKey(ctx, "worker-key-1", creator, nil)
	if err != nil {
		t.Fatalf("failed to create service key 1: %v", err)
	}

	sk2, err := d.CreateServiceKey(ctx, "worker-key-2", creator, nil)
	if err != nil {
		t.Fatalf("failed to create service key 2: %v", err)
	}

	// D. Revoke service key 2
	err = d.RevokeServiceKey(ctx, sk2.ID, creator)
	if err != nil {
		t.Fatalf("failed to revoke service key 2: %v", err)
	}

	// 4. Flush the backup journal to CAS Storage and update Slot
	if err := b.Flush(ctx); err != nil {
		t.Fatalf("failed to flush backup: %v", err)
	}
	_ = b.Close()
	_ = d.Close()

	// 5. Verify the slot was updated
	slotLink, err := memSlots.Get(ctx, slotID)
	if err != nil {
		t.Fatalf("slot not found in slots: %v", err)
	}
	if slotLink == "" {
		t.Fatalf("slot content link is empty")
	}

	// 6. Disaster Recovery / Replay into a completely blank InMemoryDiscovery
	restoredDiscovery := discovery.NewInMemoryDiscovery()

	// Initialize a reader backup pointing to the same storage and slot
	restoreDir := filepath.Join(tempDir, "restore-data")
	restoreBackup, err := NewChainedBackup(BackupOptions{
		BaseDir: restoreDir,
		Storage: memStorage,
		Slots:   memSlots,
		SlotID:  slotID,
	})
	if err != nil {
		t.Fatalf("failed to create restore backup: %v", err)
	}
	defer restoreBackup.Close()

	if err := restoreBackup.Replay(ctx, restoredDiscovery); err != nil {
		t.Fatalf("failed to replay from remote backup: %v", err)
	}

	// 7. Verify all restored state:
	// A. Service registrations
	s1, ok := restoredDiscovery.Get(ctx, "storage-1")
	if !ok || s1.Address != "http://10.0.0.1:8080" || len(s1.Tags) != 1 || s1.Tags[0] != "originals" {
		t.Fatalf("storage-1 not restored correctly: %+v", s1)
	}
	s2, ok := restoredDiscovery.Get(ctx, "kv-1")
	if !ok || s2.Address != "http://10.0.0.2:8080" {
		t.Fatalf("kv-1 not restored correctly: %+v", s2)
	}

	// B. Client registrations
	authorized, ident, err := restoredDiscovery.VerifyKey(ctx, clientPubKey)
	if err != nil || !authorized || ident == nil || ident.Email != "alice@example.com" {
		t.Fatalf("client key not restored correctly: authorized=%v, ident=%+v, err=%v", authorized, ident, err)
	}

	// C. Active service key
	valid, verifiedSK1, err := restoredDiscovery.VerifyServiceKey(ctx, sk1.Key)
	if err != nil || !valid || verifiedSK1 == nil || verifiedSK1.ID != sk1.ID {
		t.Fatalf("service key 1 not restored: valid=%v, rec=%+v, err=%v", valid, verifiedSK1, err)
	}

	// D. Revoked service key
	valid, _, err = restoredDiscovery.VerifyServiceKey(ctx, sk2.Key)
	if valid || err != discovery.ErrServiceKeyRevoked {
		t.Fatalf("expected revoked status for service key 2, got valid=%v, err=%v", valid, err)
	}
}

func TestFindOriginalsStorage(t *testing.T) {
	d := discovery.NewInMemoryDiscovery()
	ctx := context.Background()

	// 1. No originals storage registered yet
	_, err := FindOriginalsStorage(ctx, d, nil)
	if err == nil {
		t.Fatalf("expected error when no originals storage found")
	}

	// 2. Register non-originals storage
	_ = d.Register(ctx, discovery.ServiceRegistration{
		ID:        "storage-fast",
		Address:   "http://127.0.0.1:8001",
		Protocols: []string{"storage-v1"},
		Tags:      []string{"cache"},
	})
	_, err = FindOriginalsStorage(ctx, d, nil)
	if err == nil {
		t.Fatalf("expected error when no originals tag present")
	}

	// 3. Register originals storage
	_ = d.Register(ctx, discovery.ServiceRegistration{
		ID:        "storage-orig",
		Address:   "http://127.0.0.1:8002",
		Protocols: []string{"storage-v1"},
		Tags:      []string{"originals"},
	})
	s, err := FindOriginalsStorage(ctx, d, nil)
	if err != nil || s == nil {
		t.Fatalf("expected to find originals storage: %v", err)
	}
}
