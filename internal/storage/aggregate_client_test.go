package storage

import (
	"bytes"
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

	d.Register(discovery.ServiceRegistration{ID: "node1", Address: ts1.URL, Protocols: []string{"storage-v1"}})
	d.Register(discovery.ServiceRegistration{ID: "node2", Address: ts2.URL, Protocols: []string{"storage-v1"}})

	c := NewAggregateClient(nil, d, 2, 10)

	// Write operation (round-robin)
	content := []byte("hello cluster")
	addr, err := c.Store(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store error: %v", err)
	}
	if addr == "" {
		t.Fatalf("expected non-empty address")
	}

	// Because of round robin we don't know which got it, but one did.
	// Since readOperation will check live servers (which now has node1 & node2 populated by ensureLiveServers),
	// read should succeed!
	has := c.Has(addr)
	if !has {
		t.Errorf("expected to have block %s", addr)
	}

	size, ok := c.Size(addr)
	if !ok || size != int64(len(content)) {
		t.Errorf("expected size %d, got %d", len(content), size)
	}

	rc, ok := c.Get(addr)
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

	d.Register(discovery.ServiceRegistration{ID: "node1", Address: ts1.URL, Protocols: []string{"storage-v1"}})

	c := NewAggregateClient(nil, d, 2, 10)

	content := []byte("hello failover")
	addr, err := c.Store(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store error: %v", err)
	}

	// Close the server to simulate failure
	ts1.Close()

	// Wait a tiny bit (the custom transport will fail instantly on connection refused)
	time.Sleep(10 * time.Millisecond)

	// Attempting to read should fail and remove the live server
	has := c.Has(addr)
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
	d.Register(discovery.ServiceRegistration{ID: "node2", Address: ts2.URL, Protocols: []string{"storage-v1"}})

	_, err = c.Store(bytes.NewReader([]byte("new stuff")))
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

	d.Register(discovery.ServiceRegistration{ID: "node-remote", Address: ts1.URL, Protocols: []string{"storage-v1"}})

	addr, _ := store1.Store(bytes.NewReader([]byte("remote block")))

	// Finder knows about it
	f.Has("node-remote", []string{addr})

	c := NewAggregateClient(f, d, 2, 10)

	// Read should consult finder, then discovery to resolve it, then fetch it!
	has := c.Has(addr)
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
	d.Register(discovery.ServiceRegistration{ID: "bad-node", Address: ts.URL, Protocols: []string{"storage-v1"}})

	c := NewAggregateClient(nil, d, 1, 10)

	// Populate live list
	c.Store(bytes.NewReader([]byte("stuff"))) // will fail, removing node immediately!

	c.liveMu.RLock()
	count := len(c.liveIDs)
	c.liveMu.RUnlock()

	if count != 0 {
		t.Errorf("expected bad node to be removed after write failure")
	}
}
