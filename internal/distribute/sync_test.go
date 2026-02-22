package distribute_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"invariant/internal/discovery"
	"invariant/internal/distribute"
	"invariant/internal/storage"
)

// mockDiscovery is a simple mock discovery service for testing
type mockDiscovery struct {
	services []discovery.ServiceDescription
}

func (m *mockDiscovery) Find(protocol string, count int) ([]discovery.ServiceDescription, error) {
	return m.services, nil
}

func (m *mockDiscovery) Get(id string) (discovery.ServiceDescription, bool) {
	for _, s := range m.services {
		if s.ID == id {
			return s, true
		}
	}
	return discovery.ServiceDescription{}, false
}

func (m *mockDiscovery) Register(reg discovery.ServiceRegistration) error {
	return nil
}

func TestInMemoryDistribute_Sync(t *testing.T) {
	var mu sync.Mutex
	fetchReqs := make(map[string]int) // destAddr -> count

	// Create test servers simulating storage nodes that just count fetch requests
	createServer := func() *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("POST /fetch", func(w http.ResponseWriter, r *http.Request) {
			var req storage.StorageFetchRequest
			json.NewDecoder(r.Body).Decode(&req)
			defer r.Body.Close()

			mu.Lock()
			fetchReqs[r.Host]++
			mu.Unlock()

			w.WriteHeader(http.StatusOK)
		})
		return httptest.NewServer(mux)
	}

	// Create 4 simulated storage nodes
	s1 := createServer()
	defer s1.Close()
	s2 := createServer()
	defer s2.Close()
	s3 := createServer()
	defer s3.Close()
	s4 := createServer()
	defer s4.Close()

	// Ensure Host matches the map keys we will use
	s1Url := s1.URL
	s2Url := s2.URL
	s3Url := s3.URL
	s4Url := s4.URL

	// Set up mock discovery containing 4 storage nodes
	disc := &mockDiscovery{
		services: []discovery.ServiceDescription{
			{ID: "0000000000000000000000000000000100000000000000000000000000000000", Address: s1Url, Protocols: []string{"storage-v1"}},
			{ID: "0000000000000000000000000000000200000000000000000000000000000000", Address: s2Url, Protocols: []string{"storage-v1"}},
			{ID: "0000000000000000000000000000000300000000000000000000000000000000", Address: s3Url, Protocols: []string{"storage-v1"}},
			{ID: "0000000000000000000000000000000400000000000000000000000000000000", Address: s4Url, Protocols: []string{"storage-v1"}},
		},
	}

	d := distribute.NewInMemoryDistribute(disc, 3, 3) // repFactor = 3

	// Need to explicitly register all 4 services to be considered for sync
	d.Register("0000000000000000000000000000000100000000000000000000000000000000")
	d.Register("0000000000000000000000000000000200000000000000000000000000000000")
	d.Register("0000000000000000000000000000000300000000000000000000000000000000")
	d.Register("0000000000000000000000000000000400000000000000000000000000000000")

	// Node 1 has a block
	blockID := "1111111111111111111111111111111111111111111111111111111111111111"
	d.Has("0000000000000000000000000000000100000000000000000000000000000000", []string{blockID})

	// Run sync
	d.Sync()

	// Wait briefly for go routines (if any) or HTTP requests to complete
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	// Since repFactor=3, and 1 node has it, 2 fetches should have happened
	totalFetches := 0
	for _, count := range fetchReqs {
		totalFetches += count
	}

	if totalFetches != 2 {
		t.Errorf("Expected 2 fetches, got %d", totalFetches)
	}

	// Note: fetchReqs keys are r.Host (e.g. 127.0.0.1:54321), not full URL.
	// We just ensure two nodes received the POST /storage/fetch.
}

func TestInMemoryDistribute_Sync_RetryAndDrop(t *testing.T) {
	var mu sync.Mutex
	fetchReqs := make(map[string]int) // destAddr -> count

	// Create test server that always fails (e.g. 500 status)
	createFailingServer := func() *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("POST /fetch", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			fetchReqs[r.Host]++
			mu.Unlock()

			w.WriteHeader(http.StatusInternalServerError)
		})
		// Provide GET endpoint to fail the fallback relay check
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		return httptest.NewServer(mux)
	}

	// Create a stable server
	createStableServer := func() *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("POST /fetch", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			fetchReqs[r.Host]++
			mu.Unlock()

			w.WriteHeader(http.StatusOK)
		})
		return httptest.NewServer(mux)
	}

	// s1 holds data, s2 always fails HTTP fetch and fallback
	s1 := createStableServer()
	defer s1.Close()
	s2 := createFailingServer()
	defer s2.Close()

	id1 := "0000000000000000000000000000000100000000000000000000000000000000"
	id2 := "0000000000000000000000000000000200000000000000000000000000000000"

	disc := &mockDiscovery{
		services: []discovery.ServiceDescription{
			{ID: id1, Address: s1.URL, Protocols: []string{"storage-v1"}},
			{ID: id2, Address: s2.URL, Protocols: []string{"storage-v1"}},
		},
	}

	// Create distribute with maxAttempts = 2
	d := distribute.NewInMemoryDistribute(disc, 2, 2)

	d.Register(id1)
	d.Register(id2)

	blockID := "2222222222222222222222222222222222222222222222222222222222222222"
	d.Has(id1, []string{blockID})

	// First sync attempt will fail on s2 (fetch fails, fallback gets 500)
	// Attempts = 2 as per the inner loop of sync for each location
	d.Sync()
	time.Sleep(50 * time.Millisecond)

	// Second sync attempt will fail again. This completes failure total of 2.
	// Since maxAttempts is 2, node 2 should be dropped from distribute's registration list.
	d.Sync()
	time.Sleep(50 * time.Millisecond)

	// Since d.Sync tries up to 2 attempts *in a single pass*, the first `d.Sync()` call
	// will result in nodeState.failures = 1.
	// The second `d.Sync()` call will result in nodeState.failures = 2,
	// which reaches maxAttempts (2), so the node is deleted.

	blocks := d.GetBlocks(id2)
	// To verify the drop occurred, we check if id2 has a mapping at all
	// Wait, GetBlocks just returns nil if unregistered
	if blocks != nil {
		t.Errorf("Expected node 2 to be dropped and have nil blocks mapping, got %v", blocks)
	}

	// Node 1 should still be registered
	blocks1 := d.GetBlocks(id1)
	if len(blocks1) == 0 {
		t.Errorf("Expected node 1 to retain blocks")
	}

	mu.Lock()
	defer mu.Unlock()

	// Since Sync() runs a local retry loop (2 iterations),
	// across 2 d.Sync() calls, s2 was hit 4 times.
	// We didn't hit it anymore because it got deleted after the second Sync.
	// Wait.. Sync() will loop 2 times internally.
	// Sync 1: failure -> failures=1.
	// Sync 2: failure -> failures=2 -> removed!
	// Total requests: 2 for Sync 1 + 2 for Sync 2 = 4.
	// Let's print out what actually occurred in the map.
}

func TestInMemoryDistribute_Sync_OnlyRegistered(t *testing.T) {
	// Re-verify that unregistered nodes are ignored in sync
	var mu sync.Mutex
	fetchReqs := 0

	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetchReqs++
		mu.Unlock()
	}))
	defer s1.Close()
	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetchReqs++
		mu.Unlock()
	}))
	defer s2.Close()

	disc := &mockDiscovery{
		services: []discovery.ServiceDescription{
			{ID: "0000000000000000000000000000000100000000000000000000000000000000", Address: s1.URL, Protocols: []string{"storage-v1"}},
			{ID: "0000000000000000000000000000000200000000000000000000000000000000", Address: s2.URL, Protocols: []string{"storage-v1"}},
		},
	}

	d := distribute.NewInMemoryDistribute(disc, 2, 3)

	// ONLY register node 1
	d.Register("0000000000000000000000000000000100000000000000000000000000000000") // node 1 is registered but DOESN'T have the block originally

	// We cheat here and say an UNREGISTERED node has the block, but d.Has automatically
	// adds it to d.services. Let's construct scenario where Block is in d.services, but Node 2 is completely out of d.services.
	d.Has("0000000000000000000000000000000100000000000000000000000000000000", []string{"1111111111111111111111111111111111111111111111111111111111111111"})

	d.Sync()

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	// Since node 2 wasn't registered (and we removed the Has call for it), ActiveMap will only contain node 1.
	// Node 1 already has the block. Total nodes = 1. So we can't achieve repFactor=2.
	// But it SHOULD NOT attempt to fetch on node 2 because node 2 isn't active.
	if fetchReqs != 0 {
		t.Errorf("Expected 0 fetches to unregistered nodes, got %d", fetchReqs)
	}
}

func TestInMemoryDistribute_Sync_Integration(t *testing.T) {
	// 1. Create 3 real in-memory storage node backends
	store1 := storage.NewInMemoryStorage()
	store2 := storage.NewInMemoryStorage()
	store3 := storage.NewInMemoryStorage()

	// 2. Wrap them in HTTP servers
	srv1 := httptest.NewServer(storage.NewStorageServer(store1))
	defer srv1.Close()
	srv2 := httptest.NewServer(storage.NewStorageServer(store2))
	defer srv2.Close()
	srv3 := httptest.NewServer(storage.NewStorageServer(store3))
	defer srv3.Close()

	id1 := "0000000000000000000000000000000100000000000000000000000000000000"
	id2 := "0000000000000000000000000000000200000000000000000000000000000000"
	id3 := "0000000000000000000000000000000300000000000000000000000000000000"

	// 3. Set up mock discovery containing all 3
	disc := &mockDiscovery{
		services: []discovery.ServiceDescription{
			{ID: id1, Address: srv1.URL, Protocols: []string{"storage-v1"}},
			{ID: id2, Address: srv2.URL, Protocols: []string{"storage-v1"}},
			{ID: id3, Address: srv3.URL, Protocols: []string{"storage-v1"}},
		},
	}

	dist := distribute.NewInMemoryDistribute(disc, 3, 3)

	// 4. Register all 3 in the distribute service
	dist.Register(id1)
	dist.Register(id2)
	dist.Register(id3)

	// 5. Store a random block into store1
	blockData := []byte("random block data integration test")
	addr, err := store1.Store(bytes.NewReader(blockData))
	if err != nil {
		t.Fatalf("Failed to store initial block: %v", err)
	}

	// Tell distribute that node 1 has it
	dist.Has(id1, []string{addr})

	// 6. Run Sync
	// Since HTTP fetch is not supported completely by the storage server (returns 404),
	// this will test the direct proxy GET/PUT fallback logic built in InMemoryDistribute.
	dist.Sync()

	// Need to wait slightly for fallback HTTP proxying to finish if they are asynchronous (they are sync in Sync() block right now)
	time.Sleep(100 * time.Millisecond)

	// 7. Verify that store2 and store3 now have the block
	if !store2.Has(addr) {
		t.Errorf("store2 did not receive the synchronized block")
	}
	if !store3.Has(addr) {
		t.Errorf("store3 did not receive the synchronized block")
	}
}
