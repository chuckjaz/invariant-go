package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	addrA, err := cs.Store(context.Background(), strings.NewReader("12345"))
	if err != nil {
		t.Fatalf("Store A failed: %v", err)
	}

	// 2. Add block B (size: 5) -> Total: 10 (Desired size reached)
	addrB, err := cs.Store(context.Background(), strings.NewReader("abcde"))
	if err != nil {
		t.Fatalf("Store B failed: %v", err)
	}

	// 3. Keep A fresh
	hasA := cs.Has(context.Background(), addrA)
	if !hasA {
		t.Fatalf("Expected A to be present")
	}

	// 4. Add block C (size: 4) -> Total: 14 (Exceeds desired, triggers eviction)
	addrC, err := cs.Store(context.Background(), strings.NewReader("wxyz"))
	if err != nil {
		t.Fatalf("Store C failed: %v", err)
	}

	// Wait for background eviction
	time.Sleep(200 * time.Millisecond)

	// A is most recent. C is newest. B is oldest since A was touched.
	// B should be evicted.

	if local.Has(context.Background(), addrB) {
		t.Errorf("Expected block B to be evicted from local storage, but it is still there")
	}

	if !remote.Has(context.Background(), addrB) {
		t.Errorf("Expected block B to be evicted to remote storage, but it is not there")
	}

	if !local.Has(context.Background(), addrA) {
		t.Errorf("Expected block A to remain in local storage since it was recently used")
	}

	if !local.Has(context.Background(), addrC) {
		t.Errorf("Expected block C to remain in local storage since it was just added")
	}
}

func TestCachingStorageMaxSizeLimit(t *testing.T) {
	local := NewInMemoryStorage()
	cs := NewCachingStorage(local, nil, 10, 5, false)
	defer cs.Close()

	_, err := cs.Store(context.Background(), strings.NewReader("12345"))
	if err != nil {
		t.Fatalf("Store 1 failed: %v", err)
	}

	_, err = cs.Store(context.Background(), strings.NewReader("abcdef"))
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
	addrA, _ := local.Store(context.Background(), bytes.NewReader(dataA))
	local.Remove(context.Background(), addrA) // clear it so we can push through CachingStorage via StoreAt

	ok, err := cs.StoreAt(context.Background(), addrA, bytes.NewReader(dataA))
	if err != nil || !ok {
		t.Fatalf("StoreAt failed")
	}

	// Total size currently 5

	dataB := []byte("world1")
	addrB, _ := local.Store(context.Background(), bytes.NewReader(dataB))
	local.Remove(context.Background(), addrB)

	// Adding B pushes past desired size, triggers eviction of A
	cs.StoreAt(context.Background(), addrB, bytes.NewReader(dataB))

	time.Sleep(200 * time.Millisecond)

	if local.Has(context.Background(), addrA) {
		t.Errorf("Block A should have been evicted")
	}

	if !remote.Has(context.Background(), addrA) {
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
	addr, _ := remote.Store(context.Background(), bytes.NewReader(data))

	// Get from cs
	rc, ok := cs.Get(context.Background(), addr)
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
	if !local.Has(context.Background(), addr) {
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
	cs.Store(context.Background(), strings.NewReader("12345"))
	cs.Store(context.Background(), strings.NewReader("67890"))

	// Wait a moment for any async processing (though size is synchronous)
	time.Sleep(50 * time.Millisecond)

	// s.currentSize should now be 10. s.maxSize is 10.
	// The next Store should trigger s.currentSize >= s.maxSize and delegate directly.
	addrA, err := cs.Store(context.Background(), strings.NewReader("abcde"))
	if err != nil {
		t.Fatalf("Store A failed unexpectedly: %v", err)
	}

	if local.Has(context.Background(), addrA) {
		t.Errorf("Block A should not be in local storage, it should have delegated smoothly")
	}
	if !remote.Has(context.Background(), addrA) {
		t.Errorf("Block A should be in remote storage due to active delegation")
	}
}

func TestCachingStorageSync(t *testing.T) {
	local := NewInMemoryStorage()
	remote := NewInMemoryStorage()

	cs := NewCachingStorage(local, remote, 100, 50, false)
	defer cs.Close()

	// Store directly in local to simulate blocks that haven't been synced
	addrA, _ := local.Store(context.Background(), strings.NewReader("block A"))
	addrB, _ := local.Store(context.Background(), strings.NewReader("block B"))

	ctx := context.Background()
	if err := cs.Sync(ctx); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	if !remote.Has(context.Background(), addrA) || !remote.Has(context.Background(), addrB) {
		t.Errorf("Sync failed to upload blocks to destination")
	}

	// Double check destHas map
	cs.destHasMu.RLock()
	_, hasA := cs.destHas[addrA]
	_, hasB := cs.destHas[addrB]
	cs.destHasMu.RUnlock()

	if !hasA || !hasB {
		t.Errorf("Sync failed to mark blocks as present in destHas")
	}
}

func TestCachingStorageExtra(t *testing.T) {
	local := NewInMemoryStorage()
	remote := NewInMemoryStorage()
	overflow := NewInMemoryStorage()

	// Max size = 100, desired size = 50
	cs := NewCachingStorage(local, remote, 100, 50, false)
	defer cs.Close()

	// Test SetOverflow
	cs.SetOverflow(overflow)

	// Store directly to overflow
	data := []byte("overflow block data")
	addr, err := overflow.Store(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Store in overflow failed: %v", err)
	}

	// Test Has on overflow
	if !cs.Has(context.Background(), addr) {
		t.Error("Expected Has to return true for overflow block")
	}

	// Test Size on overflow
	sz, ok := cs.Size(context.Background(), addr)
	if !ok || sz != int64(len(data)) {
		t.Errorf("Expected Size to return %d (ok: true), got %d (ok: %t)", len(data), sz, ok)
	}

	// Test Get on overflow (should fetch and cache locally)
	rc, ok := cs.Get(context.Background(), addr)
	if !ok {
		t.Fatal("Expected Get to succeed for overflow block")
	}
	readData, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(readData, data) {
		t.Errorf("Read mismatch or error: err=%v, got=%q", err, readData)
	}

	// Wait for cache promotion
	time.Sleep(100 * time.Millisecond)
	if !local.Has(context.Background(), addr) {
		t.Error("Expected overflow block to be cached locally after Get")
	}

	// Test BatchStore & BatchHas
	content1 := []byte("batch-1")
	hash1 := sha256.Sum256(content1)
	addr1 := hex.EncodeToString(hash1[:])

	content2 := []byte("batch-2")
	hash2 := sha256.Sum256(content2)
	addr2 := hex.EncodeToString(hash2[:])

	blocks := map[string]io.Reader{
		addr1: bytes.NewReader(content1),
		addr2: bytes.NewReader(content2),
	}
	err = cs.BatchStore(context.Background(), blocks)
	if err != nil {
		t.Fatalf("BatchStore failed: %v", err)
	}

	missing, err := cs.BatchHas(context.Background(), []string{addr1, addr2, "b3"})
	if err != nil {
		t.Fatalf("BatchHas failed: %v", err)
	}
	if len(missing) != 1 || missing[0] != "b3" {
		t.Errorf("Expected missing to be ['b3'], got %v", missing)
	}
}
