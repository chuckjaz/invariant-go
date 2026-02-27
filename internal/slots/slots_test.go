// Package slots_test provides tests for the slots service.
package slots_test

import (
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
	_, err := client.Get(slotID)
	if err != slots.ErrSlotNotFound {
		t.Fatalf("expected ErrSlotNotFound, got %v", err)
	}

	// 3. Create new
	err = client.Create(slotID, address1)
	if err != nil {
		t.Fatalf("failed to create slot: %v", err)
	}

	// 4. Create already existing (Conflict)
	err = client.Create(slotID, address2)
	if err != slots.ErrSlotExists {
		t.Fatalf("expected ErrSlotExists, got %v", err)
	}

	// 5. Get existing
	addr, err := client.Get(slotID)
	if err != nil {
		t.Fatalf("failed to get slot: %v", err)
	}
	if addr != address1 {
		t.Fatalf("expected address %q, got %q", address1, addr)
	}

	// 6. Update with correct previous address
	err = client.Update(slotID, address2, address1)
	if err != nil {
		t.Fatalf("failed to update slot: %v", err)
	}

	// 7. Verify update
	addr, err = client.Get(slotID)
	if err != nil {
		t.Fatalf("failed to get slot: %v", err)
	}
	if addr != address2 {
		t.Fatalf("expected address %q, got %q", address2, addr)
	}

	// 8. Update with incorrect previous address (Conflict)
	err = client.Update(slotID, "hash-3", address1)
	if err != slots.ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	// 9. Verify rollback/no-change
	addr, err = client.Get(slotID)
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

	if err := fsSlots.Create(slotID, addr1); err != nil {
		t.Fatalf("failed to create block: %v", err)
	}
	if err := fsSlots.Update(slotID, addr2, addr1); err != nil {
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

	val, err := fsSlots2.Get(slotID)
	if err != nil {
		t.Fatalf("failed to get block: %v", err)
	}
	if val != addr2 {
		t.Fatalf("expected address %q, got %q", addr2, val)
	}

	// Write again to trigger snapshotting logic over time...
	if err := fsSlots2.Update(slotID, "address-3", addr2); err != nil {
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
	if err := fsSlots.Create(slotID, "val-1"); err != nil {
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

	val, err := fsSlots2.Get(slotID)
	if err != nil {
		t.Fatalf("expected to get block from snapshot, got error: %v", err)
	}
	if val != "val-1" {
		t.Fatalf("expected %q, got %q", "val-1", val)
	}
}
