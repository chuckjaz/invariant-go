package kv

import (
	"context"
	"fmt"
	"os"
	"testing"

	"invariant/internal/content"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func TestStore_BasicPutGet(t *testing.T) {
	ctx := context.Background()
	storeClient := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")

	// Create store
	s, err := NewFileKeyValueStore(ctx, slotClient, "btree-slot", nil, "journal-slot", nil, storeClient, t.TempDir(), 1000000, 2, 2, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Put "hello" -> "world"
	seq, err := s.Put(ctx, nil, "hello", []byte("world"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if seq != 1 {
		t.Errorf("Expected sequence 1, got %d", seq)
	}

	// Get "hello"
	val, _, err := s.Get(ctx, nil, "hello")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(val) != "world" {
		t.Errorf("Expected 'world', got %s", string(val))
	}

	// Put "hello" -> "again" to trigger merge (threshold = 2)
	seq2, err := s.Put(ctx, nil, "hello", []byte("again"))
	if err != nil {
		t.Fatalf("Put 2 failed: %v", err)
	}
	if seq2 != 3 {
		t.Errorf("Expected transaction ID 3, got %d", seq2)
	}

	// Get "hello" should return "again"
	val2, _, err := s.Get(ctx, nil, "hello")
	if err != nil {
		t.Fatalf("Get 2 failed: %v", err)
	}
	if string(val2) != "again" {
		t.Errorf("Expected 'again', got %s", string(val2))
	}

	// Force B-Tree retrieval by clearing cache
	s.cache = NewCache(1000)

	val3, _, err := s.Get(ctx, nil, "hello")
	if err != nil {
		t.Fatalf("Get 3 (from BTree) failed: %v", err)
	}
	if string(val3) != "again" {
		t.Errorf("Expected 'again', got %s", string(val3))
	}
}

func TestStore_BTreeSplit(t *testing.T) {
	ctx := context.Background()
	storeClient := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")

	// Create store. MaxKeys = 100, we need to insert > 100 to cause split. We set mergeThreshold = 10 to merge frequently.
	s, err := NewFileKeyValueStore(ctx, slotClient, "btree-split-slot", nil, "journal-split-slot", nil, storeClient, t.TempDir(), 1000000, 10, 10, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Insert 150 items to trigger B-Tree split
	for i := range 150 {
		key := fmt.Sprintf("key-%03d", i)
		val := fmt.Sprintf("val-%03d", i)
		_, err := s.Put(ctx, nil, key, []byte(val))
		if err != nil {
			t.Fatalf("Put failed at %d: %v", i, err)
		}
	}

	// Force B-Tree retrieval by clearing cache
	s.cache = NewCache(10)

	for i := range 150 {
		key := fmt.Sprintf("key-%03d", i)
		expected := fmt.Sprintf("val-%03d", i)
		val, _, err := s.Get(ctx, nil, key)
		if err != nil {
			t.Fatalf("Get failed at %d: %v", i, err)
		}
		if string(val) != expected {
			t.Errorf("Expected %s, got %s", expected, string(val))
		}
	}
}

func TestStore_JournalRecovery(t *testing.T) {
	ctx := context.Background()
	storeClient := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")
	journalDir := t.TempDir()

	// Create first store with very large merge threshold to keep data in journal
	s1, err := NewFileKeyValueStore(ctx, slotClient, "btree-recovery-slot", nil, "journal-recovery-slot", nil, storeClient, journalDir, 1000000, 1000, 1000, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store 1: %v", err)
	}

	// Put 10 items
	for i := range 10 {
		key := fmt.Sprintf("jkey-%d", i)
		val := fmt.Sprintf("jval-%d", i)
		_, err := s1.Put(ctx, nil, key, []byte(val))
		if err != nil {
			t.Fatalf("Put failed at %d: %v", i, err)
		}
	}
	s1.Close() // this should close journal files

	// Create second store pointing to same slots and journal directory
	s2, err := NewFileKeyValueStore(ctx, slotClient, "btree-recovery-slot", nil, "journal-recovery-slot", nil, storeClient, journalDir, 1000000, 1000, 1000, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store 2: %v", err)
	}
	defer s2.Close()

	// Ensure items are recovered
	for i := range 10 {
		key := fmt.Sprintf("jkey-%d", i)
		expected := fmt.Sprintf("jval-%d", i)
		val, _, err := s2.Get(ctx, nil, key)
		if err != nil {
			t.Fatalf("Get recovered failed at %d: %v", i, err)
		}
		if string(val) != expected {
			t.Errorf("Expected %s, got %s", expected, string(val))
		}
	}
}

func TestStore_RemoteJournalRecovery(t *testing.T) {
	ctx := context.Background()
	storeClient := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")
	journalDir := t.TempDir()

	// Create first store: large bTree merge threshold to prevent merges,
	// small journal flush threshold to force multiple uploads.
	s1, err := NewFileKeyValueStore(ctx, slotClient, "btree-remote-rec-slot", nil, "journal-remote-rec-slot", nil, storeClient, journalDir, 1000000, 1000, 2, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store 1: %v", err)
	}

	// Put 10 items. With threshold 2, this will flush and create multiple remote journal pages.
	for i := range 10 {
		key := fmt.Sprintf("remkey-%d", i)
		val := fmt.Sprintf("remval-%d", i)
		_, err := s1.Put(ctx, nil, key, []byte(val))
		if err != nil {
			t.Fatalf("Put failed at %d: %v", i, err)
		}
	}
	// Flush remaining journal entries so they become remote journals.
	s1.journal.Flush(ctx)
	s1.Close()

	// To ensure we're relying entirely on remote journals and not local ones,
	// we create the second store using a completely empty local directory.
	emptyDir := t.TempDir()

	// Create second store pointing to the same slots, but empty local directory.
	s2, err := NewFileKeyValueStore(ctx, slotClient, "btree-remote-rec-slot", nil, "journal-remote-rec-slot", nil, storeClient, emptyDir, 1000000, 1000, 2, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store 2: %v", err)
	}
	defer s2.Close()

	// Ensure items are recovered from storage
	for i := range 10 {
		key := fmt.Sprintf("remkey-%d", i)
		expected := fmt.Sprintf("remval-%d", i)
		val, _, err := s2.Get(ctx, nil, key)
		if err != nil {
			t.Fatalf("Get recovered failed at %d: %v", i, err)
		}
		if string(val) != expected {
			t.Errorf("Expected %s, got %s", expected, string(val))
		}
	}
}

func TestStore_GetHistory(t *testing.T) {
	ctx := context.Background()
	storeClient := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")

	s, err := NewFileKeyValueStore(ctx, slotClient, "btree-hist", nil, "journal-hist", nil, storeClient, t.TempDir(), 1000000, 10, 10, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Put multiple versions of "hist-key"
	for i := 1; i <= 5; i++ {
		_, err := s.Put(ctx, nil, "hist-key", fmt.Appendf(nil, "val-%d", i))
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}
	}

	// Fetch history with limit 2
	page, err := s.GetHistory(ctx, nil, "hist-key", 0, 100, 2)
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}

	if len(page.Values) != 2 {
		t.Fatalf("Expected 2 values, got %d", len(page.Values))
	}
	if string(page.Values[0].Value) != "val-5" {
		t.Errorf("Expected val-5, got %s", string(page.Values[0].Value))
	}
	if string(page.Values[1].Value) != "val-4" {
		t.Errorf("Expected val-4, got %s", string(page.Values[1].Value))
	}
	if !page.HasMore {
		t.Errorf("Expected HasMore to be true")
	}

	// Fetch remaining history
	page2, err := s.GetHistory(ctx, nil, "hist-key", 0, page.Values[1].TransactionID-1, 10)
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}
	if len(page2.Values) != 3 {
		t.Fatalf("Expected 3 values, got %d", len(page2.Values))
	}
	if string(page2.Values[2].Value) != "val-1" {
		t.Errorf("Expected val-1, got %s", string(page2.Values[2].Value))
	}
	if page2.HasMore {
		t.Errorf("Expected HasMore to be false")
	}
}

func TestStore_TransactionIsolation(t *testing.T) {
	ctx := context.Background()
	storeClient := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")

	s, err := NewFileKeyValueStore(ctx, slotClient, "btree-iso", nil, "journal-iso", nil, storeClient, t.TempDir(), 1000000, 10, 10, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Start a transaction
	tx1, err := s.StartTransaction(ctx, false)
	if err != nil {
		t.Fatalf("Failed to start transaction: %v", err)
	}

	// tx1 writes a value
	_, err = s.Put(ctx, &tx1, "iso-key", []byte("tx1-val"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Implicit read should NOT see tx1's write yet
	_, _, err = s.Get(ctx, nil, "iso-key")
	if err == nil {
		t.Fatalf("Expected error getting uncommitted key, got nil")
	}

	// tx1 should see its own write
	val, _, err := s.Get(ctx, &tx1, "iso-key")
	if err != nil {
		t.Fatalf("tx1 Get failed: %v", err)
	}
	if string(val) != "tx1-val" {
		t.Errorf("Expected 'tx1-val', got %s", string(val))
	}

	// Abort tx1
	err = s.AbortTransaction(ctx, tx1)
	if err != nil {
		t.Fatalf("Abort failed: %v", err)
	}

	// Implicit read should STILL NOT see tx1's write
	_, _, err = s.Get(ctx, nil, "iso-key")
	if err == nil {
		t.Fatalf("Expected error getting aborted key, got nil")
	}
}

func TestStore_TransactionAtomicity(t *testing.T) {
	ctx := context.Background()
	storeClient := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")

	s, err := NewFileKeyValueStore(ctx, slotClient, "btree-atom", nil, "journal-atom", nil, storeClient, t.TempDir(), 1000000, 10, 10, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// tx1 writes two keys
	tx1, err := s.StartTransaction(ctx, false)
	if err != nil {
		t.Fatalf("Failed to start transaction: %v", err)
	}

	_, err = s.Put(ctx, &tx1, "atom-key1", []byte("val1"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	_, err = s.Put(ctx, &tx1, "atom-key2", []byte("val2"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Neither is visible yet
	_, _, err = s.Get(ctx, nil, "atom-key1")
	if err == nil {
		t.Fatalf("Expected error getting uncommitted key1")
	}
	_, _, err = s.Get(ctx, nil, "atom-key2")
	if err == nil {
		t.Fatalf("Expected error getting uncommitted key2")
	}

	// Commit tx1
	err = s.CommitTransaction(ctx, tx1)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Both should be visible now
	v1, _, err := s.Get(ctx, nil, "atom-key1")
	if err != nil || string(v1) != "val1" {
		t.Errorf("Expected val1, got %s (err: %v)", string(v1), err)
	}
	v2, _, err := s.Get(ctx, nil, "atom-key2")
	if err != nil || string(v2) != "val2" {
		t.Errorf("Expected val2, got %s (err: %v)", string(v2), err)
	}
}

func TestStore_TransactionConsistency(t *testing.T) {
	ctx := context.Background()
	storeClient := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")

	s, err := NewFileKeyValueStore(ctx, slotClient, "btree-cons", nil, "journal-cons", nil, storeClient, t.TempDir(), 1000000, 10, 10, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Initial setup
	_, err = s.Put(ctx, nil, "account1", []byte("100"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// tx1 starts (sequential)
	tx1, err := s.StartTransaction(ctx, true)
	if err != nil {
		t.Fatalf("Failed to start tx1: %v", err)
	}

	// tx2 starts (sequential)
	tx2, err := s.StartTransaction(ctx, true)
	if err != nil {
		t.Fatalf("Failed to start tx2: %v", err)
	}

	// tx1 reads "account1"
	v1, _, err := s.Get(ctx, &tx1, "account1")
	if err != nil || string(v1) != "100" {
		t.Fatalf("tx1 Get failed: %v", err)
	}

	// tx2 reads "account1"
	v2, _, err := s.Get(ctx, &tx2, "account1")
	if err != nil || string(v2) != "100" {
		t.Fatalf("tx2 Get failed: %v", err)
	}

	// tx1 writes "account1" = "101"
	_, err = s.Put(ctx, &tx1, "account1", []byte("101"))
	if err != nil {
		t.Fatalf("tx1 Put failed: %v", err)
	}

	// tx1 commits successfully
	err = s.CommitTransaction(ctx, tx1)
	if err != nil {
		t.Fatalf("tx1 Commit failed: %v", err)
	}

	// tx2 writes "account2" = "1" (dependent on reading account1)
	_, err = s.Put(ctx, &tx2, "account2", []byte("1"))
	if err != nil {
		t.Fatalf("tx2 Put failed: %v", err)
	}

	// tx2 commit should fail because its read set ("account1") was modified by tx1 after tx2 started
	err = s.CommitTransaction(ctx, tx2)
	if err == nil {
		t.Fatalf("Expected tx2 commit to fail due to conflict, but it succeeded")
	}

	// Ensure tx2's writes are not visible
	_, _, err = s.Get(ctx, nil, "account2")
	if err == nil {
		t.Fatalf("Expected account2 to not exist since tx2 aborted due to conflict")
	}
}

func TestStore_TransactionAbortPreservesOldValue(t *testing.T) {
	ctx := context.Background()
	storeClient := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")

	s, err := NewFileKeyValueStore(ctx, slotClient, "btree-abort-pres", nil, "journal-abort-pres", nil, storeClient, t.TempDir(), 1000000, 10, 10, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Initial committed value
	_, err = s.Put(ctx, nil, "shared-key", []byte("old-valid-value"))
	if err != nil {
		t.Fatalf("Initial put failed: %v", err)
	}

	// Start tx1 that modifies the key
	tx1, err := s.StartTransaction(ctx, false)
	if err != nil {
		t.Fatalf("Failed to start tx1: %v", err)
	}

	_, err = s.Put(ctx, &tx1, "shared-key", []byte("new-aborted-value"))
	if err != nil {
		t.Fatalf("tx1 put failed: %v", err)
	}

	// Abort tx1
	err = s.AbortTransaction(ctx, tx1)
	if err != nil {
		t.Fatalf("Abort failed: %v", err)
	}

	// Fetch key; should get the old value, not an error and not the aborted value
	val, _, err := s.Get(ctx, nil, "shared-key")
	if err != nil {
		t.Fatalf("Get failed after abort: %v", err)
	}

	if string(val) != "old-valid-value" {
		t.Errorf("Expected 'old-valid-value', got '%s'", string(val))
	}
}

func TestStore_GetHistoryMerging(t *testing.T) {
	ctx := context.Background()
	storeClient := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")

	s, err := NewFileKeyValueStore(ctx, slotClient, "btree-hist-merging", nil, "journal-hist-merging", nil, storeClient, t.TempDir(), 1000000, 10, 10, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Put a value to trigger transaction initialization
	_, err = s.Put(ctx, nil, "mkey", []byte("mval1"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Manually inject merging state
	s.mu.Lock()
	s.isMerging = true
	s.mergingRecords = []Record{
		{
			Type:          RecordTypePut,
			Key:           "mkey",
			Value:         []byte("mval-merging"),
			TransactionID: 2,
		},
	}
	s.mergingIndex = map[string][]int{
		"mkey": {0},
	}
	delete(s.pendingIndex, "mkey")
	s.cache.mu.Lock()
	delete(s.cache.items, "mkey")
	s.cache.mu.Unlock()
	s.mu.Unlock()

	// Call GetHistory. It should read from mergingRecords!
	page, err := s.GetHistory(ctx, nil, "mkey", 0, 100, 10)
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}

	// We expect the merging record to be found!
	foundMerging := false
	for _, val := range page.Values {
		if string(val.Value) == "mval-merging" {
			foundMerging = true
		}
	}
	if !foundMerging {
		t.Errorf("Expected to find the merging record, but it was not returned. Values: %+v", page.Values)
	}

	// Call Get. It should also read from mergingRecords!
	val, txID, err := s.Get(ctx, nil, "mkey")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(val) != "mval-merging" || txID != 2 {
		t.Errorf("Expected 'mval-merging' at txID 2, got '%s' at txID %d", string(val), txID)
	}
}

type mockSlotsGetError struct {
	slots.Slots
	getError error
	getData  string
}

func (m *mockSlotsGetError) Get(ctx context.Context, id string) (string, error) {
	if m.getError != nil {
		return "", m.getError
	}
	return m.getData, nil
}

type mockSlotsMulti struct {
	slots.Slots
	getFunc func(ctx context.Context, id string) (string, error)
}

func (m *mockSlotsMulti) Get(ctx context.Context, id string) (string, error) {
	if m.getFunc != nil {
		return m.getFunc(ctx, id)
	}
	return "", slots.ErrSlotNotFound
}

func TestStore_NewFileKeyValueStore_Errors(t *testing.T) {
	ctx := context.Background()
	storeClient := storage.NewInMemoryStorage()

	// 1. slotClient.Get returns error on bTree root
	badSlot1 := &mockSlotsGetError{
		Slots:    slots.NewMemorySlots("test"),
		getError: fmt.Errorf("simulated get error"),
	}
	_, err := NewFileKeyValueStore(ctx, badSlot1, "btree-slot", nil, "journal-slot", nil, storeClient, t.TempDir(), 1000000, 10, 10, content.WriterOptions{})
	if err == nil {
		t.Errorf("Expected error when slotClient.Get fails on bTree root, got nil")
	}

	// 2. bTree root slot has invalid JSON
	badSlot2 := &mockSlotsGetError{
		Slots:   slots.NewMemorySlots("test"),
		getData: "{invalid json",
	}
	_, err = NewFileKeyValueStore(ctx, badSlot2, "btree-slot", nil, "journal-slot", nil, storeClient, t.TempDir(), 1000000, 10, 10, content.WriterOptions{})
	if err == nil {
		t.Errorf("Expected error when B-tree root slot has invalid JSON, got nil")
	}

	// 3. slotClient.Get returns error on journal slot
	badSlot3 := &mockSlotsMulti{
		Slots: slots.NewMemorySlots("test"),
		getFunc: func(ctx context.Context, id string) (string, error) {
			if id == "btree-slot" {
				return "", slots.ErrSlotNotFound
			}
			return "", fmt.Errorf("simulated journal get error")
		},
	}
	_, err = NewFileKeyValueStore(ctx, badSlot3, "btree-slot", nil, "journal-slot", nil, storeClient, t.TempDir(), 1000000, 10, 10, content.WriterOptions{})
	if err == nil {
		t.Errorf("Expected error when slotClient.Get fails on journal slot, got nil")
	}

	// 4. journal slot has invalid JSON
	badSlot4 := &mockSlotsMulti{
		Slots: slots.NewMemorySlots("test"),
		getFunc: func(ctx context.Context, id string) (string, error) {
			if id == "btree-slot" {
				return "", slots.ErrSlotNotFound
			}
			return "{invalid json", nil
		},
	}
	_, err = NewFileKeyValueStore(ctx, badSlot4, "btree-slot", nil, "journal-slot", nil, storeClient, t.TempDir(), 1000000, 10, 10, content.WriterOptions{})
	if err == nil {
		t.Errorf("Expected error when journal slot has invalid JSON, got nil")
	}

	// 5. NewJournal error (e.g. invalid journal baseDir)
	tempFile, err := os.CreateTemp("", "journal-file-err")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())
	tempFile.Close()

	emptySlot := slots.NewMemorySlots("test")
	_, err = NewFileKeyValueStore(ctx, emptySlot, "btree-slot", nil, "journal-slot", nil, storeClient, tempFile.Name(), 1000000, 10, 10, content.WriterOptions{})
	if err == nil {
		t.Errorf("Expected error when NewJournal fails due to file path, got nil")
	}

	// 6. bTree root loadNode error
	badSlot6 := &mockSlotsMulti{
		Slots: slots.NewMemorySlots("test"),
		getFunc: func(ctx context.Context, id string) (string, error) {
			if id == "btree-slot" {
				return `{"addr":"sha256:0000000000000000000000000000000000000000000000000000000000000000"}`, nil
			}
			return "", slots.ErrSlotNotFound
		},
	}
	_, err = NewFileKeyValueStore(ctx, badSlot6, "btree-slot", nil, "journal-slot", nil, storeClient, t.TempDir(), 1000000, 10, 10, content.WriterOptions{})
	if err != nil {
		t.Errorf("Expected store creation to succeed even if B-Tree root fails to load, but got: %v", err)
	}
}
