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
	s, err := NewStore(ctx, slotClient, "btree-slot", nil, "journal-slot", nil, storeClient, t.TempDir(), 1000000, 2, 2, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Put "hello" -> "world"
	seq, err := s.Put(ctx, "hello", []byte("world"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if seq != 1 {
		t.Errorf("Expected sequence 1, got %d", seq)
	}

	// Get "hello"
	val, err := s.Get(ctx, "hello")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(val) != "world" {
		t.Errorf("Expected 'world', got %s", string(val))
	}

	// Put "hello" -> "again" to trigger merge (threshold = 2)
	seq2, err := s.Put(ctx, "hello", []byte("again"))
	if err != nil {
		t.Fatalf("Put 2 failed: %v", err)
	}
	if seq2 != 2 {
		t.Errorf("Expected sequence 2, got %d", seq2)
	}

	// Get "hello" should return "again"
	val2, err := s.Get(ctx, "hello")
	if err != nil {
		t.Fatalf("Get 2 failed: %v", err)
	}
	if string(val2) != "again" {
		t.Errorf("Expected 'again', got %s", string(val2))
	}

	// Force B-Tree retrieval by clearing cache
	s.cache = NewCache(1000)

	val3, err := s.Get(ctx, "hello")
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
	s, err := NewStore(ctx, slotClient, "btree-split-slot", nil, "journal-split-slot", nil, storeClient, t.TempDir(), 1000000, 10, 10, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// Insert 150 items to trigger B-Tree split
	for i := range 150 {
		key := fmt.Sprintf("key-%03d", i)
		val := fmt.Sprintf("val-%03d", i)
		_, err := s.Put(ctx, key, []byte(val))
		if err != nil {
			t.Fatalf("Put failed at %d: %v", i, err)
		}
	}

	// Force B-Tree retrieval by clearing cache
	s.cache = NewCache(10)

	for i := range 150 {
		key := fmt.Sprintf("key-%03d", i)
		expected := fmt.Sprintf("val-%03d", i)
		val, err := s.Get(ctx, key)
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
	s1, err := NewStore(ctx, slotClient, "btree-recovery-slot", nil, "journal-recovery-slot", nil, storeClient, journalDir, 1000000, 1000, 1000, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store 1: %v", err)
	}

	// Put 10 items
	for i := range 10 {
		key := fmt.Sprintf("jkey-%d", i)
		val := fmt.Sprintf("jval-%d", i)
		_, err := s1.Put(ctx, key, []byte(val))
		if err != nil {
			t.Fatalf("Put failed at %d: %v", i, err)
		}
	}
	s1.Close() // this should close journal files

	// Create second store pointing to same slots and journal directory
	s2, err := NewStore(ctx, slotClient, "btree-recovery-slot", nil, "journal-recovery-slot", nil, storeClient, journalDir, 1000000, 1000, 1000, content.WriterOptions{})
	if err != nil {
		t.Fatalf("Failed to create store 2: %v", err)
	}
	defer s2.Close()

	// Ensure items are recovered
	for i := range 10 {
		key := fmt.Sprintf("jkey-%d", i)
		expected := fmt.Sprintf("jval-%d", i)
		val, err := s2.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get recovered failed at %d: %v", i, err)
		}
		if string(val) != expected {
			t.Errorf("Expected %s, got %s", expected, string(val))
		}
	}
}
