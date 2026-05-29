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

func TestCache_EvictIfNeededFallback(t *testing.T) {
	// Cache max size = 20 bytes
	cache := NewCache(20)

	rec1 := Record{Key: "key1", TransactionID: 1, Value: []byte("1234567890")}
	rec2 := Record{Key: "key2", TransactionID: 2, Value: []byte("1234567890")}
	rec3 := Record{Key: "key3", TransactionID: 3, Value: []byte("1234567890")}

	// 1. Add rec1 (not evictable)
	cache.Add(rec1, false)
	// 2. Add rec2 (evictable)
	cache.Add(rec2, true)

	// 3. Add rec3 (evictable). Size becomes 30 > 20, eviction is triggered.
	// Since rec1 (back of LRU) is not evictable, it should search back and evict rec2.
	cache.Add(rec3, true)

	// Verify key1 is still in cache
	if _, ok := cache.Get("key1"); !ok {
		t.Errorf("Expected key1 (non-evictable) to remain in cache")
	}

	// Verify key2 was evicted (since it was the only evictable item)
	if _, ok := cache.Get("key2"); ok {
		t.Errorf("Expected key2 to be evicted, but it is still in cache")
	}

	// Verify key3 is in cache
	if _, ok := cache.Get("key3"); !ok {
		t.Errorf("Expected key3 to be in cache")
	}

	// Yield to let any background goroutines from Get complete their list operations
	time.Sleep(50 * time.Millisecond)

	// 4. Force no-evictable break path
	// Add key4 (not evictable) when size is 20. Total size will grow to 30.
	// Both items remaining (key1 and key3) are not evictable (we mark key3 as non-evictable by adding it again or invalidating its B-Tree flag)
	rec3Update := rec3
	rec3Update.TransactionID = 13 // newer txID to bypass the Cache.Add transaction check
	cache.Add(rec3Update, false)  // successfully update key3 to non-evictable

	rec4 := Record{Key: "key4", TransactionID: 4, Value: []byte("1234567890")}
	cache.Add(rec4, false) // total size 30, no items can be evicted

	// Verify no items were evicted because none are evictable
	cache.mu.Lock()
	for k, it := range cache.items {
		item := it.Value.(*cacheItem)
		t.Logf("CACHE KEY: %s, inBTree: %t, txID: %d", k, item.inBTree, item.txID)
	}
	cache.mu.Unlock()

	if _, ok := cache.Get("key1"); !ok {
		t.Errorf("Expected key1 to remain")
	}
	if _, ok := cache.Get("key3"); !ok {
		t.Errorf("Expected key3 to remain")
	}
	if _, ok := cache.Get("key4"); !ok {
		t.Errorf("Expected key4 to remain")
	}
}
