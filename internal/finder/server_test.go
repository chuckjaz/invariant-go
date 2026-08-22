package finder

import (
	"bytes"
	"context"
	"fmt"
	"invariant/internal/discovery"
	"invariant/internal/notify"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// In memory implementations of Discovery for testing
type mockDiscovery struct {
	services map[string]discovery.ServiceDescription
}

func newMockDiscovery() *mockDiscovery {
	return &mockDiscovery{
		services: make(map[string]discovery.ServiceDescription),
	}
}

func (m *mockDiscovery) Get(ctx context.Context, id string) (discovery.ServiceDescription, bool) {
	desc, ok := m.services[id]
	return desc, ok
}

func (m *mockDiscovery) Find(ctx context.Context, protocol, tag string, count int) ([]discovery.ServiceDescription, error) {
	var results []discovery.ServiceDescription
	for _, desc := range m.services {
		hasProtocol := protocol == ""
		for _, p := range desc.Protocols {
			if p == protocol {
				hasProtocol = true
				break
			}
		}
		hasTag := tag == ""
		for _, t := range desc.Tags {
			if t == tag {
				hasTag = true
				break
			}
		}
		if hasProtocol && hasTag {
			results = append(results, desc)
			if len(results) >= count {
				break
			}
		}
	}
	return results, nil
}

func (m *mockDiscovery) Register(ctx context.Context, reg discovery.ServiceRegistration) error {
	m.services[reg.ID] = discovery.ServiceDescription{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: reg.Protocols,
		Tags:      reg.Tags,
	}
	return nil
}

func TestFinderHasAndFindBlock(t *testing.T) {
	disc := newMockDiscovery()

	// 1. Create a finder service
	selfIDStr := "1111111111111111111111111111111111111111111111111111111111111111"
	f, _ := NewMemoryFinder(selfIDStr)
	server := NewFinderServer(f, disc)

	ts := httptest.NewServer(server)
	defer ts.Close()

	client := NewClient(ts.URL, nil)

	// Block address
	blockAddr := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	// 2. Client queries the finder - shouldn't find block
	res, err := client.Find(context.Background(), blockAddr)
	if err != nil {
		t.Fatalf("Failed to find: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("Expected 0 results initially, got %d", len(res))
	}

	// 3. Register a block from a simulated storage node
	storageID := "storage-1"
	reqBody := notify.NotifyRequest{Addresses: []string{blockAddr}}
	err = client.Notify(context.Background(), storageID, reqBody.Addresses)

	// 4. Client queries - should now find the block on storage-1
	res, err = client.Find(context.Background(), blockAddr)
	if err != nil {
		t.Fatalf("Failed to find: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(res))
	}

	expected := FindResponse{
		ID:       storageID,
		Protocol: "storage-v1",
	}
	if res[0] != expected {
		t.Errorf("Expected %v, got %v", expected, res[0])
	}
}

func TestFinderPeerAndPushBlocks(t *testing.T) {
	disc := newMockDiscovery()

	// We are testing: Finder A has a block. Finder B notifies Finder A.
	// Finder A examines distances and decides that Finder B is closer to the block.
	// Finder A then pushes the block ID to Finder B via 'Has'.

	blockAddr := "0000000000000000000000000000000000000000000000000000000000000002"

	// Finder A is distance 3 from block (XOR: 2 ^ 1 = 3)
	idA := "0000000000000000000000000000000000000000000000000000000000000001"
	fA, _ := NewMemoryFinder(idA)
	serverA := NewFinderServer(fA, disc)
	tsA := httptest.NewServer(serverA)
	defer tsA.Close()

	// Finder B is distance 2 from block (XOR: 2 ^ 0 = 2).
	// So B is closer to block than A.
	idB := "0000000000000000000000000000000000000000000000000000000000000000"
	fB, _ := NewMemoryFinder(idB)
	serverB := NewFinderServer(fB, disc)
	tsB := httptest.NewServer(serverB)
	defer tsB.Close()

	// Register Finder B in discovery so Finder A can push to it
	disc.Register(context.Background(), discovery.ServiceRegistration{
		ID:        idB,
		Address:   tsB.URL,
		Protocols: []string{"finder-v1"},
	})

	// Tell Finder A that storage-1 has the block
	clientA := NewClient(tsA.URL, nil)
	clientA.Notify(context.Background(), "storage-1", []string{blockAddr})

	// Notify Finder A about Peer Finder B
	fmt.Printf("Peering A about B\n")
	err := clientA.Peer(context.Background(), idB)
	if err != nil {
		t.Fatalf("Failed to notify: %v", err)
	}

	// Give a moment for the background goroutine to push the block from A to B
	time.Sleep(100 * time.Millisecond)

	// Now ask Finder B if it knows about the block.
	// It should respond that "storage-1" has it.
	clientB := NewClient(tsB.URL, nil)
	res, err := clientB.Find(context.Background(), blockAddr)
	if err != nil {
		t.Fatalf("Failed to find on B: %v", err)
	}

	if len(res) != 1 {
		t.Fatalf("Expected Finder B to know about the block. Got %d responses", len(res))
	}
	if res[0].ID != "storage-1" {
		t.Errorf("Expected Finder B to know storage-1 has it, got %s", res[0].ID)
	}
}

func TestFinderReturnsFinders(t *testing.T) {
	disc := newMockDiscovery()

	idA := "0000000000000000000000000000000000000000000000000000000000000001"
	fA, _ := NewMemoryFinder(idA)
	serverA := NewFinderServer(fA, disc)
	tsA := httptest.NewServer(serverA)
	defer tsA.Close()

	// Add B and C to A's routing table
	idB := "0000000000000000000000000000000000000000000000000000000000000002"
	idC := "0000000000000000000000000000000000000000000000000000000000000003"

	clientA := NewClient(tsA.URL, nil)

	// Register them in discovery (though Peer doesn't strictly need this unless it's pushing blocks)
	disc.Register(context.Background(), discovery.ServiceRegistration{ID: idB, Address: "http://b", Protocols: []string{"finder-v1"}})
	disc.Register(context.Background(), discovery.ServiceRegistration{ID: idC, Address: "http://c", Protocols: []string{"finder-v1"}})

	// Notify A about Peer B and C
	clientA.Peer(context.Background(), idB)
	clientA.Peer(context.Background(), idC)

	// Now ask A for a block it DOES NOT know about. It should return its closest finders.
	blockAddr := "0000000000000000000000000000000000000000000000000000000000000000"

	res, err := clientA.Find(context.Background(), blockAddr)
	if err != nil {
		t.Fatalf("Failed to find: %v", err)
	}

	if len(res) != 2 {
		t.Fatalf("Expected 2 finders in response, got %d", len(res))
	}

	// Verify they are returned as finder-v1 protocols
	for _, r := range res {
		if r.Protocol != "finder-v1" {
			t.Errorf("Expected protocol finder-v1, got %s", r.Protocol)
		}
		if r.ID != idB && r.ID != idC {
			t.Errorf("Unexpected finder ID: %s", r.ID)
		}
	}
}

func TestFinderServer_Errors(t *testing.T) {
	disc := newMockDiscovery()
	selfIDStr := "1111111111111111111111111111111111111111111111111111111111111111"
	f, _ := NewMemoryFinder(selfIDStr)
	server := NewFinderServer(f, disc)

	ts := httptest.NewServer(server)
	defer ts.Close()

	client := NewClient(ts.URL, nil)

	// 1. Client ID() should return empty string
	if id := client.ID(); id != "" {
		t.Errorf("Expected empty string for client.ID(), got %q", id)
	}

	// 2. GET /id
	resp, err := ts.Client().Get(ts.URL + "/id")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 OK for /id, got %d", resp.StatusCode)
	}

	// 3. GET /address with invalid format (contains 'z')
	resp, err = ts.Client().Get(ts.URL + "/zz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request for invalid address, got %d", resp.StatusCode)
	}

	// 4. PUT /notify/{id} with invalid JSON
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/notify/storage1", bytes.NewBuffer([]byte("{invalid json")))
	resp, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request for invalid JSON notify, got %d", resp.StatusCode)
	}

	// 5. PUT /peer/{id} with invalid peer ID
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/peer/invalid-peer-id", nil)
	resp, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request for invalid peer ID, got %d", resp.StatusCode)
	}
}
