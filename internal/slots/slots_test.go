// Package slots_test provides tests for the slots service.
package slots_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"invariant/internal/slots"
)

func runEndToEndTest(t *testing.T, service slots.Slots) {
	server := slots.NewServer(service)

	ts := httptest.NewServer(server)
	defer ts.Close()

	client := slots.NewClient(ts.URL, ts.Client())

	// 1. Check ID
	if id := client.ID(); id != service.ID() {
		t.Fatalf("expected id %q, got %q", service.ID(), id)
	}

	slotID := "slot-123"
	address1 := "hash-1"
	address2 := "hash-2"

	// 2. Get non-existent
	_, err := client.Get(context.Background(), slotID)
	if err != slots.ErrSlotNotFound {
		t.Fatalf("expected ErrSlotNotFound, got %v", err)
	}

	// 3. Create new
	err = client.Create(context.Background(), slotID, address1, "")
	if err != nil {
		t.Fatalf("failed to create slot: %v", err)
	}

	// 4. Create already existing (Conflict)
	err = client.Create(context.Background(), slotID, address2, "")
	if err != slots.ErrSlotExists {
		t.Fatalf("expected ErrSlotExists, got %v", err)
	}

	// 5. Get existing
	addr, err := client.Get(context.Background(), slotID)
	if err != nil {
		t.Fatalf("failed to get slot: %v", err)
	}
	if addr != address1 {
		t.Fatalf("expected address %q, got %q", address1, addr)
	}

	// 6. Update with correct previous address
	err = client.Update(context.Background(), slotID, address2, address1, nil)
	if err != nil {
		t.Fatalf("failed to update slot: %v", err)
	}

	// 7. Verify update
	addr, err = client.Get(context.Background(), slotID)
	if err != nil {
		t.Fatalf("failed to get slot: %v", err)
	}
	if addr != address2 {
		t.Fatalf("expected address %q, got %q", address2, addr)
	}

	// 8. Update with incorrect previous address (Conflict)
	err = client.Update(context.Background(), slotID, "hash-3", address1, nil)
	if err != slots.ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	// 9. Verify rollback/no-change
	addr, err = client.Get(context.Background(), slotID)
	if err != nil {
		t.Fatalf("failed to get slot: %v", err)
	}
	if addr != address2 {
		t.Fatalf("expected address %q, got %q", address2, addr)
	}
}

func TestSlots_MemoryEndToEnd(t *testing.T) {
	memorySlots := slots.NewMemorySlots("test-memory-slots-id")
	runEndToEndTest(t, memorySlots)
}

func TestSlots_FileSystemEndToEnd(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "slots_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	fsSlots, err := slots.NewFileSystemSlots(tempDir, time.Hour)
	if err != nil {
		t.Fatalf("failed to create fs slots: %v", err)
	}
	defer fsSlots.Close()

	runEndToEndTest(t, fsSlots)
}

func TestSlots_FileSystemPersistence(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "slots_test_persistence")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	fsSlots, err := slots.NewFileSystemSlots(tempDir, time.Millisecond*50)
	if err != nil {
		t.Fatalf("failed to create fs slots: %v", err)
	}

	slotID := "persistent-slot"
	addr1 := "address-1"
	addr2 := "address-2"

	if err := fsSlots.Create(context.Background(), slotID, addr1, ""); err != nil {
		t.Fatalf("failed to create block: %v", err)
	}
	if err := fsSlots.Update(context.Background(), slotID, addr2, addr1, nil); err != nil {
		t.Fatalf("failed to update block: %v", err)
	}

	id := fsSlots.ID()
	fsSlots.Close()

	// Wait a moment for things to settle
	time.Sleep(time.Millisecond * 100)

	// Re-open and verify
	fsSlots2, err := slots.NewFileSystemSlots(tempDir, time.Hour)
	if err != nil {
		t.Fatalf("failed to open fs slots again: %v", err)
	}
	defer fsSlots2.Close()

	if fsSlots2.ID() != id {
		t.Fatalf("expected id %q, got %q", id, fsSlots2.ID())
	}

	val, err := fsSlots2.Get(context.Background(), slotID)
	if err != nil {
		t.Fatalf("failed to get block: %v", err)
	}
	if val != addr2 {
		t.Fatalf("expected address %q, got %q", addr2, val)
	}

	// Write again to trigger snapshotting logic over time...
	if err := fsSlots2.Update(context.Background(), slotID, "address-3", addr2, nil); err != nil {
		t.Fatalf("failed to update block: %v", err)
	}
}

func TestSlots_FileSystemSnapshots(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "slots_test_snapshots")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Very short snapshot interval
	fsSlots, err := slots.NewFileSystemSlots(tempDir, time.Millisecond*100)
	if err != nil {
		t.Fatalf("failed to create fs slots: %v", err)
	}

	slotID := "snapshot-slot"
	if err := fsSlots.Create(context.Background(), slotID, "val-1", ""); err != nil {
		t.Fatalf("failed to create block: %v", err)
	}

	// Give enough time for a snapshot
	time.Sleep(time.Millisecond * 250)

	// Check if snapshot exists
	_, err = os.Stat(filepath.Join(tempDir, "snapshot.json"))
	if err != nil {
		t.Fatalf("expected snapshot.json to exist, got error: %v", err)
	}

	fsSlots.Close()

	// Reopen with long snapshot interface to test loading snapshot
	fsSlots2, err := slots.NewFileSystemSlots(tempDir, time.Hour)
	if err != nil {
		t.Fatalf("failed to load fs slots from snapshot: %v", err)
	}
	defer fsSlots2.Close()

	val, err := fsSlots2.Get(context.Background(), slotID)
	if err != nil {
		t.Fatalf("expected to get block from snapshot, got error: %v", err)
	}
	if val != "val-1" {
		t.Fatalf("expected %q, got %q", "val-1", val)
	}
}

type mockNotifyClient struct {
	notified chan []string
}

func (m *mockNotifyClient) Notify(id string, batch []string) error {
	m.notified <- batch
	return nil
}

func TestSlots_Extra(t *testing.T) {
	ctx := context.Background()

	// 1. Setup inmemory slots
	memSlots := slots.NewMemorySlots("mem-1")

	// Subscribe before creating slots to avoid missing notification
	subCh := memSlots.Subscribe(ctx)
	if subCh == nil {
		t.Fatal("Subscribe returned nil channel")
	}

	// Create slot
	err := memSlots.Create(ctx, "s1", "addr1", "")
	if err != nil {
		t.Fatalf("failed to create slot: %v", err)
	}

	// Verify Subscribe
	select {
	case id := <-subCh:
		if id != "s1" {
			t.Errorf("Expected subscription for s1, got %q", id)
		}
	default:
		t.Error("Expected slot subscription notification, got none")
	}

	// Call List after creating slot to avoid locking deadlocks
	chList := memSlots.List(ctx, 1)
	if chList == nil {
		t.Fatal("List returned nil channel")
	}

	// Verify List
	select {
	case batch := <-chList:
		if len(batch) != 1 || batch[0] != "s1" {
			t.Errorf("Expected List batch containing s1, got %v", batch)
		}
	case <-time.After(1 * time.Second):
		t.Error("Expected List batch notification, got none")
	}

	// Verify List empty on client
	server := slots.NewServer(memSlots)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	client := slots.NewClient(ts.URL, ts.Client())
	cList := client.List(ctx, 10)
	for range cList {
	}
	cSub := client.Subscribe(ctx)
	for range cSub {
	}

	// 2. Test update errors (unauthorized policy ecc)
	pubKey, privKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	eccID := hex.EncodeToString(pubKey)

	err = memSlots.Create(ctx, eccID, "ecc-addr1", "ecc")
	if err != nil {
		t.Fatalf("failed to create ecc slot: %v", err)
	}

	// Try updating without correct signature
	err = memSlots.Update(ctx, eccID, "ecc-addr2", "ecc-addr1", []byte("bad-signature"))
	if err != slots.ErrUnauthorized {
		t.Errorf("Expected ErrUnauthorized, got %v", err)
	}

	// Update with correct signature
	reqData, _ := json.Marshal(slots.SlotUpdate{
		Address:         "ecc-addr2",
		PreviousAddress: "ecc-addr1",
	})
	sig := ed25519.Sign(privKey, reqData)

	err = memSlots.Update(ctx, eccID, "ecc-addr2", "ecc-addr1", sig)
	if err != nil {
		t.Fatalf("failed to update ecc slot with valid signature: %v", err)
	}

	// Verify update
	eccVal, err := memSlots.Get(ctx, eccID)
	if err != nil || eccVal != "ecc-addr2" {
		t.Errorf("Failed to retrieve updated ecc slot: val=%s, err=%v", eccVal, err)
	}

	// Update with conflict on existing
	reqDataConflict, _ := json.Marshal(slots.SlotUpdate{
		Address:         "ecc-addr3",
		PreviousAddress: "ecc-addr-conflict",
	})
	sigConflict := ed25519.Sign(privKey, reqDataConflict)
	err = memSlots.Update(ctx, eccID, "ecc-addr3", "ecc-addr-conflict", sigConflict)
	if err != slots.ErrConflict {
		t.Errorf("Expected ErrConflict on mismatch previousAddress, got %v", err)
	}

	// 3. Test FileSystemSlots List, Subscribe and ECC policies
	tempDir, err := os.MkdirTemp("", "fs_slots_extra")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	fsSlots, err := slots.NewFileSystemSlots(tempDir, time.Hour)
	if err != nil {
		t.Fatalf("failed to create fs slots: %v", err)
	}
	defer fsSlots.Close()

	// Subscribe before creating slots to avoid missing notification
	fsSub := fsSlots.Subscribe(ctx)

	err = fsSlots.Create(ctx, "fs1", "fs-addr1", "")
	if err != nil {
		t.Fatalf("failed to create fs slot: %v", err)
	}

	select {
	case id := <-fsSub:
		if id != "fs1" {
			t.Errorf("Expected subscription for fs1, got %q", id)
		}
	default:
		t.Error("Expected fs slot subscription notification")
	}

	// Call List after creating slot to avoid locking deadlocks
	fsList := fsSlots.List(ctx, 1)

	select {
	case batch := <-fsList:
		if len(batch) != 1 || batch[0] != "fs1" {
			t.Errorf("Expected fs list containing fs1, got %v", batch)
		}
	case <-time.After(1 * time.Second):
		t.Error("Expected fs list batch notification")
	}

	// FS update conflict when slot not found
	err = fsSlots.Update(ctx, "non-existent-fs", "addr", "prev", nil)
	if err != slots.ErrSlotNotFound {
		t.Errorf("Expected ErrSlotNotFound, got %v", err)
	}

	// Test ECC policy on fsSlots
	err = fsSlots.Create(ctx, eccID, "fs-ecc-addr1", "ecc")
	if err != nil {
		t.Fatalf("failed to create fs ecc slot: %v", err)
	}

	err = fsSlots.Update(ctx, eccID, "fs-ecc-addr2", "fs-ecc-addr1", []byte("bad-signature"))
	if err != slots.ErrUnauthorized {
		t.Errorf("Expected ErrUnauthorized on fs ecc update, got %v", err)
	}

	reqDataFS, _ := json.Marshal(slots.SlotUpdate{
		Address:         "fs-ecc-addr2",
		PreviousAddress: "fs-ecc-addr1",
	})
	sigFS := ed25519.Sign(privKey, reqDataFS)
	err = fsSlots.Update(ctx, eccID, "fs-ecc-addr2", "fs-ecc-addr1", sigFS)
	if err != nil {
		t.Fatalf("failed to update fs ecc slot: %v", err)
	}

	// 4. Test Server StartNotification
	notifyCl := &mockNotifyClient{
		notified: make(chan []string, 10),
	}

	// Start notifications with batchSize=1, duration=10ms
	server.StartNotification(ctx, []slots.NotifyClient{notifyCl}, 1, 10*time.Millisecond)

	// Should notify about the existing slots immediately (s1 and eccID, in any order)
	existing := make(map[string]bool)
	for range 2 {
		select {
		case batch := <-notifyCl.notified:
			if len(batch) != 1 {
				t.Errorf("Expected batch of size 1, got %v", batch)
			} else {
				existing[batch[0]] = true
			}
		case <-time.After(200 * time.Millisecond):
			t.Error("Timed out waiting for server notification")
		}
	}
	if !existing["s1"] || !existing[eccID] {
		t.Errorf("Expected initial notifications for s1 and %s, got %v", eccID, existing)
	}

	// Create another slot to trigger subscribe notification in StartNotification
	err = memSlots.Create(ctx, "s2", "addr2", "")
	if err != nil {
		t.Fatalf("failed to create slot s2: %v", err)
	}

	select {
	case batch := <-notifyCl.notified:
		if len(batch) != 1 || batch[0] != "s2" {
			t.Errorf("Expected notification for s2, got %v", batch)
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("Timed out waiting for server subscription notification")
	}
}

func TestSlots_ClientServerErrors(t *testing.T) {
	ctx := context.Background()

	// 1. Test NewClient with nil httpClient
	cNil := slots.NewClient("http://localhost:12345", nil)
	if cNil == nil {
		t.Fatal("Expected client not to be nil")
	}

	// 2. Client ID/Get/Update/Create with invalid URL
	cBad := slots.NewClient("http://[::1]:invalidport", nil)
	if id := cBad.ID(); id != "" {
		t.Errorf("Expected empty ID for invalid URL, got %q", id)
	}
	if _, err := cBad.Get(ctx, "s1"); err == nil {
		t.Error("Expected error from Get with invalid URL")
	}
	if err := cBad.Update(ctx, "s1", "addr", "prev", nil); err == nil {
		t.Error("Expected error from Update with invalid URL")
	}
	if err := cBad.Create(ctx, "s1", "addr", ""); err == nil {
		t.Error("Expected error from Create with invalid URL")
	}

	// 3. Server returning non-200/unexpected status codes
	errServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer errServer.Close()

	cErr := slots.NewClient(errServer.URL, errServer.Client())
	if id := cErr.ID(); id != "" {
		t.Errorf("Expected empty ID on server error, got %q", id)
	}
	if _, err := cErr.Get(ctx, "s1"); err == nil {
		t.Error("Expected error from Get on server 500")
	}
	if err := cErr.Update(ctx, "s1", "addr", "prev", nil); err == nil {
		t.Error("Expected error from Update on server 500")
	}
	if err := cErr.Create(ctx, "s1", "addr", ""); err == nil {
		t.Error("Expected error from Create on server 500")
	}

	// 4. Server handling bad JSON requests
	memSlots := slots.NewMemorySlots("mem-err")
	server := slots.NewServer(memSlots)
	ts := httptest.NewServer(server)
	defer ts.Close()

	// Send bad JSON to PUT /s1 (Update)
	reqUpdate, _ := http.NewRequest(http.MethodPut, ts.URL+"/s1", bytes.NewReader([]byte("{bad json")))
	respUpdate, err := ts.Client().Do(reqUpdate)
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	respUpdate.Body.Close()
	if respUpdate.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected status Bad Request (400) for malformed update, got %d", respUpdate.StatusCode)
	}

	// Send bad JSON to POST /s1 (Create)
	reqCreate, _ := http.NewRequest(http.MethodPost, ts.URL+"/s1", bytes.NewReader([]byte("{bad json")))
	respCreate, err := ts.Client().Do(reqCreate)
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	respCreate.Body.Close()
	if respCreate.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected status Bad Request (400) for malformed create, got %d", respCreate.StatusCode)
	}
}
