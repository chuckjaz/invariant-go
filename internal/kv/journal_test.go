package kv

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"invariant/internal/content"
	"invariant/internal/slots"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

func TestJournal_GettersAndSetters(t *testing.T) {
	ctx := context.Background()
	store := newInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")
	tempDir := t.TempDir()

	j, err := NewJournal(tempDir, store, slotClient, "j-slot", nil, nil, 10, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create journal: %v", err)
	}
	defer j.Close()

	if j.PreviousJournal() != nil {
		t.Errorf("Expected nil previous journal link, got %+v", j.PreviousJournal())
	}

	dummyLink := &content.ContentLink{Address: "sha256:dummy"}
	j.SetPreviousJournal(dummyLink)

	if j.PreviousJournal() == nil || j.PreviousJournal().Address != "sha256:dummy" {
		t.Errorf("Expected dummy link, got %+v", j.PreviousJournal())
	}

	// Verify LastRecordType defaults to RecordTypePut or TxStart
	// Append a RecordTypeTxStart
	rec := Record{Type: RecordTypeTxStart, TransactionID: 100}
	flushed, err := j.Append(ctx, rec)
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}
	if flushed {
		t.Errorf("Did not expect flush on first write")
	}

	if j.LastRecordType() != RecordTypeTxStart {
		t.Errorf("Expected LastRecordType to be RecordTypeTxStart, got %v", j.LastRecordType())
	}
}

func TestJournal_AutoFlush(t *testing.T) {
	ctx := context.Background()
	store := newInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")
	tempDir := t.TempDir()

	// maxEntries = 2 (first write is header, second is record, third triggers flush? No, entries count increments on Record append)
	// Let's trace NewJournal:
	// - NewJournal opens file, writes header, and does file.Sync()
	// - entries count is reset to 0 in openNewFile()
	// - j.Append increments j.entries. When entries >= maxEntries, it flushes.
	// So with maxEntries = 2:
	// Append 1: entries = 1 (no flush)
	// Append 2: entries = 2 (flushes!)
	j, err := NewJournal(tempDir, store, slotClient, "j-slot", nil, nil, 2, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create journal: %v", err)
	}
	defer j.Close()

	// Append first record
	rec1 := Record{Type: RecordTypePut, Key: "k1", Value: []byte("v1"), TransactionID: 1}
	flushed1, err := j.Append(ctx, rec1)
	if err != nil {
		t.Fatalf("Append 1 failed: %v", err)
	}
	if flushed1 {
		t.Errorf("Expected flushed=false on first append, got true")
	}

	// Append second record; this should trigger flush and return flushed=true
	rec2 := Record{Type: RecordTypeTxCommit, Key: "", Value: nil, TransactionID: 1}
	flushed2, err := j.Append(ctx, rec2)
	if err != nil {
		t.Fatalf("Append 2 failed: %v", err)
	}
	if !flushed2 {
		t.Errorf("Expected flushed=true on second append, got false")
	}

	// The slots should have been updated with the new journal pointer
	var slotVal string
	for range 50 {
		slotVal, err = slotClient.Get(ctx, "j-slot")
		if err == nil && slotVal != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Failed to retrieve slot: %v", err)
	}
	if slotVal == "" {
		t.Errorf("Expected slot to contain the flushed journal content link, got empty")
	}
}

func TestJournal_LoadLocalJournals_Error(t *testing.T) {
	// Directly construct a journal with an invalid directory to verify LoadLocalJournals error path
	j := &Journal{
		baseDir: "/nonexistent-directory-path-should-fail",
	}

	_, err := j.LoadLocalJournals()
	if err == nil {
		t.Errorf("Expected error when loading from nonexistent directory, got nil")
	}
}

func TestJournal_CloseMultipleTimes(t *testing.T) {
	store := newInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")
	tempDir := t.TempDir()

	j, err := NewJournal(tempDir, store, slotClient, "j-slot", nil, nil, 10, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create journal: %v", err)
	}

	// First close: closes currentFile
	if err := j.Close(); err != nil {
		t.Errorf("First close failed: %v", err)
	}

	// Second close: should return an error since underlying file is already closed
	if err := j.Close(); err == nil {
		t.Errorf("Expected error on second close, got nil")
	}

	// Manually set currentFile to nil and close to cover nil branch
	j.currentFile = nil
	if err := j.Close(); err != nil {
		t.Errorf("Close on nil currentFile failed: %v", err)
	}
}

func TestJournal_AppendWithUserInfo(t *testing.T) {
	ctx := context.Background()
	store := newInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")
	tempDir := t.TempDir()

	j, err := NewJournal(tempDir, store, slotClient, "j-slot", nil, nil, 10, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create journal: %v", err)
	}
	defer j.Close()

	// 1. Prepare WhoIsResponse with UserProfile
	whois := &apitype.WhoIsResponse{
		UserProfile: &tailcfg.UserProfile{
			ID: 98765,
		},
	}
	ctx = ContextWithWhoIs(ctx, whois)

	rec := Record{Type: RecordTypePut, Key: "k1", Value: []byte("v1"), TransactionID: 42}
	_, err = j.Append(ctx, rec)
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	if err := j.Close(); err != nil {
		t.Fatalf("Failed to close journal: %v", err)
	}

	// 2. Load the journal records and check if UserID is populated
	records, err := j.LoadLocalJournals()
	if err != nil {
		t.Fatalf("Failed to load local journals: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("Expected 1 record, got %d", len(records))
	}

	if records[0].UserID != "userid:98765" {
		t.Errorf("Expected UserID to be 'userid:98765', got %q", records[0].UserID)
	}
}

func TestJournal_LoadLocalJournals_IgnoresNonJournalFiles(t *testing.T) {
	ctx := context.Background()
	store := newInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")
	tempDir := t.TempDir()

	j, err := NewJournal(tempDir, store, slotClient, "j-slot", nil, nil, 10, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create journal: %v", err)
	}
	defer j.Close()

	// Append valid record
	rec := Record{Type: RecordTypePut, Key: "validKey", Value: []byte("validVal"), TransactionID: 1}
	if _, err := j.Append(ctx, rec); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Create non-journal files in baseDir (e.g. "id", "notes.txt", "journal.bak")
	os.WriteFile(filepath.Join(tempDir, "id"), []byte("non-json-id-data"), 0644)
	os.WriteFile(filepath.Join(tempDir, "notes.txt"), []byte("some arbitrary text"), 0644)
	os.WriteFile(filepath.Join(tempDir, "journal.bak"), []byte("backup"), 0644)

	// Flush buffer by closing
	if err := j.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	records, err := j.LoadLocalJournals()
	if err != nil {
		t.Fatalf("LoadLocalJournals failed: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("Expected 1 record, got %d", len(records))
	}
	if records[0].Key != "validKey" {
		t.Errorf("Expected key 'validKey', got %q", records[0].Key)
	}
}
