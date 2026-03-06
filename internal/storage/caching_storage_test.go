package storage

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestCachingStorageLRUEviction(t *testing.T) {
	local := NewInMemoryStorage()
	remote := NewInMemoryStorage()

	// Max size = 15, desired size = 10
	cs := NewCachingStorage(local, remote, 15, 10, false)
	defer cs.Close()

	// 1. Add block A (size: 5)
	addrA, err := cs.Store(strings.NewReader("12345"))
	if err != nil {
		t.Fatalf("Store A failed: %v", err)
	}

	// 2. Add block B (size: 5) -> Total: 10 (Desired size reached)
	addrB, err := cs.Store(strings.NewReader("abcde"))
	if err != nil {
		t.Fatalf("Store B failed: %v", err)
	}

	// 3. Keep A fresh
	hasA := cs.Has(addrA)
	if !hasA {
		t.Fatalf("Expected A to be present")
	}

	// 4. Add block C (size: 4) -> Total: 14 (Exceeds desired, triggers eviction)
	addrC, err := cs.Store(strings.NewReader("wxyz"))
	if err != nil {
		t.Fatalf("Store C failed: %v", err)
	}

	// Wait for background eviction
	time.Sleep(200 * time.Millisecond)

	// A is most recent. C is newest. B is oldest since A was touched.
	// B should be evicted.

	if local.Has(addrB) {
		t.Errorf("Expected block B to be evicted from local storage, but it is still there")
	}

	if !remote.Has(addrB) {
		t.Errorf("Expected block B to be evicted to remote storage, but it is not there")
	}

	if !local.Has(addrA) {
		t.Errorf("Expected block A to remain in local storage since it was recently used")
	}

	if !local.Has(addrC) {
		t.Errorf("Expected block C to remain in local storage since it was just added")
	}
}

func TestCachingStorageMaxSizeLimit(t *testing.T) {
	local := NewInMemoryStorage()
	cs := NewCachingStorage(local, nil, 10, 5, false)
	defer cs.Close()

	_, err := cs.Store(strings.NewReader("12345"))
	if err != nil {
		t.Fatalf("Store 1 failed: %v", err)
	}

	_, err = cs.Store(strings.NewReader("abcdef"))
	if err != ErrMaxSizeExceeded {
		t.Fatalf("Expected ErrMaxSizeExceeded for block that would push size past max, got %v", err)
	}
}

func TestCachingStorageStoreAtEvictionTrigger(t *testing.T) {
	local := NewInMemoryStorage()
	remote := NewInMemoryStorage()
	cs := NewCachingStorage(local, remote, 15, 5, false)
	defer cs.Close()

	// Just use valid fake hash for simplicity since InMemoryStorage verifies sha256.
	// Store via normal Store to get valid address for StoreAt
	dataA := []byte("hello")
	addrA, _ := local.Store(bytes.NewReader(dataA))
	local.Remove(addrA) // clear it so we can push through CachingStorage via StoreAt

	ok, err := cs.StoreAt(addrA, bytes.NewReader(dataA))
	if err != nil || !ok {
		t.Fatalf("StoreAt failed")
	}

	// Total size currently 5

	dataB := []byte("world1")
	addrB, _ := local.Store(bytes.NewReader(dataB))
	local.Remove(addrB)

	// Adding B pushes past desired size, triggers eviction of A
	cs.StoreAt(addrB, bytes.NewReader(dataB))

	time.Sleep(200 * time.Millisecond)

	if local.Has(addrA) {
		t.Errorf("Block A should have been evicted")
	}

	if !remote.Has(addrA) {
		t.Errorf("Block A should be on remote")
	}
}

func TestCachingStorageTeeGet(t *testing.T) {
	local := NewInMemoryStorage()
	remote := NewInMemoryStorage()
	cs := NewCachingStorage(local, remote, 100, 50, false)
	defer cs.Close()

	// Store in remote only
	data := []byte("hello destination")
	addr, _ := remote.Store(bytes.NewReader(data))

	// Get from cs
	rc, ok := cs.Get(addr)
	if !ok {
		t.Fatalf("Expected block to be retrievable from destination")
	}

	readData, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(readData) != string(data) {
		t.Fatalf("Failed to read correctly: %v", err)
	}

	// wait for goroutine to finish tee-ing
	time.Sleep(100 * time.Millisecond)

	// Local should now have it!
	if !local.Has(addr) {
		t.Errorf("Expected block to be cached in local storage")
	}
}

func TestCachingStorageDelegateOnMax(t *testing.T) {
	local := NewInMemoryStorage()
	remote := NewInMemoryStorage()
	// Set desiredSize == maxSize so eviction doesn't aggressively shrink the cache beneath maxSize
	cs := NewCachingStorage(local, remote, 10, 10, true) // delegateOnMax = true
	defer cs.Close()

	// 1. Fill cache exactly to maxSize
	cs.Store(strings.NewReader("12345"))
	cs.Store(strings.NewReader("67890"))

	// Wait a moment for any async processing (though size is synchronous)
	time.Sleep(50 * time.Millisecond)

	// s.currentSize should now be 10. s.maxSize is 10.
	// The next Store should trigger s.currentSize >= s.maxSize and delegate directly.
	addrA, err := cs.Store(strings.NewReader("abcde"))
	if err != nil {
		t.Fatalf("Store A failed unexpectedly: %v", err)
	}

	if local.Has(addrA) {
		t.Errorf("Block A should not be in local storage, it should have delegated smoothly")
	}
	if !remote.Has(addrA) {
		t.Errorf("Block A should be in remote storage due to active delegation")
	}
}
