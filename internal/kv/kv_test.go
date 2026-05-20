package kv

import (
	"context"
	"testing"

	"invariant/internal/slots"
	"invariant/internal/storage"
)

func TestStore_BasicPutGet(t *testing.T) {
	ctx := context.Background()
	storeClient := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")
	
	// Create store
	s, err := NewStore(ctx, slotClient, "test-slot", nil, storeClient, t.TempDir(), 1000000, 2, 2)
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
