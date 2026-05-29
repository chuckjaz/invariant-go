package kv

import (
	"context"
	"fmt"
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
