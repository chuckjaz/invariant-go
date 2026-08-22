package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"invariant/internal/discovery"
	"invariant/internal/finder"
)

// setupTestServer creates a real httptest server wrapping an InMemoryStorage
func setupTestServer() (*httptest.Server, Storage) {
	memStore := NewInMemoryStorage()
	server := NewStorageServer(memStore)
	ts := httptest.NewServer(server.Handler())
	return ts, memStore
}

func TestAggregateClient_StoreAndRead(t *testing.T) {
	d := discovery.NewInMemoryDiscovery()
	// Create two real storage servers
	ts1, _ := setupTestServer()
	defer ts1.Close()
	ts2, _ := setupTestServer()
	defer ts2.Close()

	d.Register(context.Background(), discovery.ServiceRegistration{ID: "node1", Address: ts1.URL, Protocols: []string{"storage-v1", "batch-storage-v1"}})
	d.Register(context.Background(), discovery.ServiceRegistration{ID: "node2", Address: ts2.URL, Protocols: []string{"storage-v1", "batch-storage-v1"}})

	c := NewAggregateClient(nil, d, 2, 10)

	// Write operation (round-robin)
	content := []byte("hello cluster")
	addr, err := c.Store(context.Background(), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store error: %v", err)
	}
	if addr == "" {
		t.Fatalf("expected non-empty address")
	}

	// Because of round robin we don't know which got it, but one did.
	// Since readOperation will check live servers (which now has node1 & node2 populated by ensureLiveServers),
	// read should succeed!
	has := c.Has(context.Background(), addr)
	if !has {
		t.Errorf("expected to have block %s", addr)
	}

	size, ok := c.Size(context.Background(), addr)
	if !ok || size != int64(len(content)) {
		t.Errorf("expected size %d, got %d", len(content), size)
	}

	rc, ok := c.Get(context.Background(), addr)
	if !ok {
		t.Fatalf("expected GET to succeed")
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != string(content) {
		t.Errorf("expected content %s, got %s", content, data)
	}
}

func TestAggregateClient_LiveServerFailure(t *testing.T) {
	d := discovery.NewInMemoryDiscovery()
	ts1, _ := setupTestServer()

	d.Register(context.Background(), discovery.ServiceRegistration{ID: "node1", Address: ts1.URL, Protocols: []string{"storage-v1", "batch-storage-v1"}})

	c := NewAggregateClient(nil, d, 2, 10)

	content := []byte("hello failover")
	addr, err := c.Store(context.Background(), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store error: %v", err)
	}

	// Close the server to simulate failure
	ts1.Close()

	// Wait a tiny bit (the custom transport will fail instantly on connection refused)
	time.Sleep(10 * time.Millisecond)

	// Attempting to read should fail and remove the live server
	has := c.Has(context.Background(), addr)
	if has {
		t.Errorf("expected false for dead server")
	}

	c.liveMu.RLock()
	count := len(c.liveIDs)
	c.liveMu.RUnlock()

	if count != 0 {
		t.Errorf("expected dead server to be removed from liveIDs, got %d", count)
	}

	// Now try to store again. It should requery discovery.
	// We add a new server to discovery.
	ts2, _ := setupTestServer()
	defer ts2.Close()
	d.Register(context.Background(), discovery.ServiceRegistration{ID: "node2", Address: ts2.URL, Protocols: []string{"storage-v1", "batch-storage-v1"}})

	_, err = c.Store(context.Background(), bytes.NewReader([]byte("new stuff")))
	if err != nil {
		t.Fatalf("expected store to succeed after requerying discovery: %v", err)
	}

	c.liveMu.RLock()
	count = len(c.liveIDs)
	c.liveMu.RUnlock()

	if count == 0 {
		t.Errorf("expected live servers to be populated again")
	}
}

func TestAggregateClient_FinderFallback(t *testing.T) {
	d := discovery.NewInMemoryDiscovery()
	f, err := finder.NewMemoryFinder("0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatalf("failed to create memory finder: %v", err)
	}

	// A node exists but is NOT in our live lists
	ts1, store1 := setupTestServer()
	defer ts1.Close()

	d.Register(context.Background(), discovery.ServiceRegistration{ID: "node-remote", Address: ts1.URL, Protocols: []string{"storage-v1", "batch-storage-v1"}})

	addr, _ := store1.Store(context.Background(), bytes.NewReader([]byte("remote block")))

	// Finder knows about it
	f.Notify(context.Background(), "node-remote", []string{addr})

	c := NewAggregateClient(f, d, 2, 10)

	// Read should consult finder, then discovery to resolve it, then fetch it!
	has := c.Has(context.Background(), addr)
	if !has {
		t.Fatalf("expected finder fallback to discover storage server")
	}

	// It should now be cached in LRU and live list!
	c.liveMu.RLock()
	count := len(c.liveIDs)
	c.liveMu.RUnlock()

	if count != 1 {
		t.Errorf("expected remote node to be dynamically added to live list, got %d", count)
	}

	// LRU check
	srvs := c.getServersForBlock(addr)
	if len(srvs) != 1 || srvs[0] != "node-remote" {
		t.Errorf("expected LRU to remember node-remote, got %v", srvs)
	}
}

func TestAggregateClient_LRUEviction(t *testing.T) {
	c := NewAggregateClient(nil, nil, 0, 2)

	c.markBlockUsed("addr1", []string{"node1"})
	c.markBlockUsed("addr2", []string{"node2"})
	c.markBlockUsed("addr3", []string{"node3"}) // This should evict addr1

	if srvs := c.getServersForBlock("addr1"); len(srvs) != 0 {
		t.Errorf("expected addr1 to be evicted")
	}
	if srvs := c.getServersForBlock("addr2"); len(srvs) == 0 {
		t.Errorf("expected addr2 to be present")
	}
	if srvs := c.getServersForBlock("addr3"); len(srvs) == 0 {
		t.Errorf("expected addr3 to be present")
	}
}

// Handler that closes connection arbitrarily to simulate a bad server
func TestAggregateClient_BadTransportHandling(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Immediately drop
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	defer ts.Close()

	d := discovery.NewInMemoryDiscovery()
	d.Register(context.Background(), discovery.ServiceRegistration{ID: "bad-node", Address: ts.URL, Protocols: []string{"storage-v1", "batch-storage-v1"}})

	c := NewAggregateClient(nil, d, 1, 10)

	// Populate live list
	c.Store(context.Background(), bytes.NewReader([]byte("stuff"))) // will fail, removing node immediately!

	c.liveMu.RLock()
	count := len(c.liveIDs)
	c.liveMu.RUnlock()

	if count != 0 {
		t.Errorf("expected bad node to be removed after write failure")
	}
}

type mockSyncStorage struct {
	*InMemoryStorage
	syncCount int
}

func (m *mockSyncStorage) Sync(ctx context.Context) error {
	m.syncCount++
	return nil
}

func TestAggregateClient_Sync(t *testing.T) {
	c := NewAggregateClient(nil, nil, 0, 10)

	mock := &mockSyncStorage{InMemoryStorage: NewInMemoryStorage()}

	c.liveMu.Lock()
	c.liveServers["mock1"] = liveServerEntry{client: mock, supportsBatch: false}
	c.liveIDs = []string{"mock1"}
	c.liveMu.Unlock()

	// Should not sync anything yet (no writes)
	ctx := context.Background()
	c.Sync(ctx)
	if mock.syncCount != 0 {
		t.Errorf("expected 0 syncs, got %d", mock.syncCount)
	}

	// Perform a write
	_, err := c.Store(context.Background(), bytes.NewReader([]byte("test data")))
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Now it should sync
	c.Sync(ctx)
	if mock.syncCount != 1 {
		t.Errorf("expected 1 sync, got %d", mock.syncCount)
	}

	// Call again with no new writes, shouldn't sync mock again
	c.Sync(ctx)
	if mock.syncCount != 1 {
		t.Errorf("expected still 1 sync, got %d", mock.syncCount)
	}
}

func TestAggregateClient_Batch(t *testing.T) {
	ctx := context.Background()

	// 1. Setup with a client that supports batch
	d := discovery.NewInMemoryDiscovery()
	ts1, _ := setupTestServer()
	defer ts1.Close()
	d.Register(ctx, discovery.ServiceRegistration{ID: "node1", Address: ts1.URL, Protocols: []string{"storage-v1", "batch-storage-v1"}})

	c := NewAggregateClient(nil, d, 1, 10)

	// Test StoreAt
	content := []byte("storeat aggregate data")
	addrA, err := c.Store(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	ok, err := c.StoreAt(ctx, addrA, bytes.NewReader(content))
	if err != nil || !ok {
		t.Errorf("StoreAt failed: err=%v, ok=%t", err, ok)
	}

	// Test BatchStore (supports batch)
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
	err = c.BatchStore(ctx, blocks)
	if err != nil {
		t.Fatalf("BatchStore (supportsBatch) failed: %v", err)
	}

	// Test BatchHas (supports batch)
	missing, err := c.BatchHas(ctx, []string{addr1, addr2, "b3"})
	if err != nil {
		t.Fatalf("BatchHas (supportsBatch) failed: %v", err)
	}
	if len(missing) != 1 || missing[0] != "b3" {
		t.Errorf("Expected missing to be ['b3'], got %v", missing)
	}

	// 2. Setup with a client that does NOT support batch
	d2 := discovery.NewInMemoryDiscovery()
	ts2, _ := setupTestServer()
	defer ts2.Close()
	d2.Register(ctx, discovery.ServiceRegistration{ID: "node2", Address: ts2.URL, Protocols: []string{"storage-v1"}}) // no batch-storage-v1

	c2 := NewAggregateClient(nil, d2, 1, 10)

	// Test BatchStore (does NOT support batch)
	blocksNoBatch := map[string]io.Reader{
		addr1: bytes.NewReader(content1),
		addr2: bytes.NewReader(content2),
	}
	err = c2.BatchStore(ctx, blocksNoBatch)
	if err != nil {
		t.Fatalf("BatchStore (no supportsBatch) failed: %v", err)
	}

	// Test BatchHas (does NOT support batch)
	missing2, err := c2.BatchHas(ctx, []string{addr1, addr2, "b3"})
	if err != nil {
		t.Fatalf("BatchHas (no supportsBatch) failed: %v", err)
	}
	if len(missing2) != 1 || missing2[0] != "b3" {
		t.Errorf("Expected missing to be ['b3'], got %v", missing2)
	}
}

func TestAggregateClient_WriteTagRestriction(t *testing.T) {
	ctx := context.Background()
	d := discovery.NewInMemoryDiscovery()

	tsFast, memFast := setupTestServer()
	defer tsFast.Close()

	tsSlow, memSlow := setupTestServer()
	defer tsSlow.Close()

	// Register two storage servers: one with tag "fast" and one with tag "slow"
	d.Register(ctx, discovery.ServiceRegistration{
		ID:        "node-fast",
		Address:   tsFast.URL,
		Protocols: []string{"storage-v1", "batch-storage-v1"},
		Tags:      []string{"fast"},
	})
	d.Register(ctx, discovery.ServiceRegistration{
		ID:        "node-slow",
		Address:   tsSlow.URL,
		Protocols: []string{"storage-v1", "batch-storage-v1"},
		Tags:      []string{"slow"},
	})

	// Pre-populate a block directly on slow server
	slowOnlyContent := []byte("slow-only-data")
	slowHash := sha256.Sum256(slowOnlyContent)
	slowAddr := hex.EncodeToString(slowHash[:])
	_, _ = memSlow.StoreAt(ctx, slowAddr, bytes.NewReader(slowOnlyContent))

	// 1. Create client restricted to writeTag "fast"
	client := NewAggregateClient(nil, d, 2, 10, WithWriteTagOption("fast"))
	if client.WriteTag() != "fast" {
		t.Errorf("expected WriteTag to be 'fast', got %q", client.WriteTag())
	}

	// Write data using Store
	fastContent := []byte("fast-data-1")
	addr1, err := client.Store(ctx, bytes.NewReader(fastContent))
	if err != nil {
		t.Fatalf("Store failed with writeTag 'fast': %v", err)
	}

	// Verify data was written to memFast and NOT to memSlow
	if !memFast.Has(ctx, addr1) {
		t.Errorf("expected block %s to be written to fast server", addr1)
	}
	if memSlow.Has(ctx, addr1) {
		t.Errorf("expected block %s NOT to be written to slow server", addr1)
	}

	// Verify reads work for both:
	// - fast data on fast server
	if !client.Has(ctx, addr1) {
		t.Errorf("expected client.Has to find block on fast server")
	}
	// - slow data on slow server (reads are not restricted by write tag)
	if !client.Has(ctx, slowAddr) {
		t.Errorf("expected client.Has to find pre-existing block on slow server")
	}

	// 2. Test BatchStore with writeTag "fast"
	batchContent := []byte("batch-fast-data")
	batchHash := sha256.Sum256(batchContent)
	batchAddr := hex.EncodeToString(batchHash[:])
	err = client.BatchStore(ctx, map[string]io.Reader{batchAddr: bytes.NewReader(batchContent)})
	if err != nil {
		t.Fatalf("BatchStore failed with writeTag 'fast': %v", err)
	}
	if !memFast.Has(ctx, batchAddr) {
		t.Errorf("expected batch block to be written to fast server")
	}
	if memSlow.Has(ctx, batchAddr) {
		t.Errorf("expected batch block NOT to be written to slow server")
	}

	// 3. Switch write tag dynamically using SetWriteTag / WithWriteTag
	client.SetWriteTag("slow")
	if client.WriteTag() != "slow" {
		t.Errorf("expected WriteTag to be 'slow', got %q", client.WriteTag())
	}

	slowContent2 := []byte("slow-data-2")
	addr2, err := client.Store(ctx, bytes.NewReader(slowContent2))
	if err != nil {
		t.Fatalf("Store failed after switching writeTag to 'slow': %v", err)
	}
	if !memSlow.Has(ctx, addr2) {
		t.Errorf("expected block %s to be written to slow server", addr2)
	}
	if memFast.Has(ctx, addr2) {
		t.Errorf("expected block %s NOT to be written to fast server", addr2)
	}

	// 4. Test nonexistent tag: writes should fail with ErrNoLiveServers
	client.SetWriteTag("nonexistent-tag")
	_, err = client.Store(ctx, bytes.NewReader([]byte("should-fail")))
	if err == nil {
		t.Errorf("expected Store to fail with nonexistent writeTag, got nil")
	}
}
