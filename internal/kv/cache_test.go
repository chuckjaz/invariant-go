package kv

import (
	"testing"
	"time"
)

func TestCache_LRUEviction(t *testing.T) {
	// Create a cache with a max size of 20 bytes.
	// We'll insert records where each value is 10 bytes.
	// This means the cache can hold at most 2 items.
	cache := NewCache(20)

	rec1 := Record{Key: "key1", TransactionID: 1, Value: []byte("1234567890")}
	rec2 := Record{Key: "key2", TransactionID: 2, Value: []byte("1234567890")}
	rec3 := Record{Key: "key3", TransactionID: 3, Value: []byte("1234567890")}

	// Add 1 and 2. Set inBTree=true so they are eligible for eviction.
	cache.Add(rec1, true)
	cache.Add(rec2, true)

	// Cache should now have key1 and key2.
	// Read key1, which should schedule a background task to move it to the front (most recently used).
	if _, ok := cache.Get("key1"); !ok {
		t.Fatalf("Expected key1 to be in cache")
	}

	// Yield and wait briefly to ensure the background goroutine completes its MoveToFront
	time.Sleep(50 * time.Millisecond)

	// Add key3. The cache size will push to 30, requiring an eviction.
	// Since key1 was recently read, key2 is now the least recently used (at the back of the LRU list).
	// Therefore, key2 should be evicted, and key1 should remain.
	cache.Add(rec3, true)

	// Verify key1 is still in the cache (it was recently used)
	if _, ok := cache.Get("key1"); !ok {
		t.Errorf("Expected key1 to remain in cache (recently used), but it was evicted")
	}

	// Verify key2 was evicted (it was the least recently used)
	if _, ok := cache.Get("key2"); ok {
		t.Errorf("Expected key2 to be evicted (least recently used), but it is still in cache")
	}

	// Verify key3 was successfully added
	if _, ok := cache.Get("key3"); !ok {
		t.Errorf("Expected key3 to be in cache")
	}
}
