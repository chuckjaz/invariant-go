package finder

import (
	"fmt"
	"invariant/internal/discovery"
	"invariant/internal/has"
	"net/http/httptest"
	"reflect"
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

func (m *mockDiscovery) Get(id string) (discovery.ServiceDescription, bool) {
	desc, ok := m.services[id]
	return desc, ok
}

func (m *mockDiscovery) Find(protocol string, count int) ([]discovery.ServiceDescription, error) {
	var results []discovery.ServiceDescription
	for _, desc := range m.services {
		for _, p := range desc.Protocols {
			if p == protocol {
				results = append(results, desc)
			}
		}
	}
	return results, nil
}

func (m *mockDiscovery) Register(reg discovery.ServiceRegistration) error {
	m.services[reg.ID] = discovery.ServiceDescription{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: reg.Protocols,
	}
	return nil
}

func TestFinderHasAndFindBlock(t *testing.T) {
	disc := newMockDiscovery()

	// 1. Create a finder service
	selfIDStr := "1111111111111111111111111111111111111111111111111111111111111111"
	f, _ := NewMemoryFinder(selfIDStr)
	server := NewFinderServer(f, disc)

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	client := NewClient(ts.URL, nil)

	// Block address
	blockAddr := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	// 2. Client queries the finder - shouldn't find block
	res, err := client.Find(blockAddr)
	if err != nil {
		t.Fatalf("Failed to find: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("Expected 0 results initially, got %d", len(res))
	}

	// 3. Register a block from a simulated storage node
	storageID := "storage-1"
	reqBody := has.HasRequest{Addresses: []string{blockAddr}}
	err = client.Has(storageID, reqBody.Addresses)

	// 4. Client queries - should now find the block on storage-1
	res, err = client.Find(blockAddr)
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
	if !reflect.DeepEqual(res[0], expected) {
		t.Errorf("Expected %v, got %v", expected, res[0])
	}
}

func TestFinderNotifyAndPushBlocks(t *testing.T) {
	disc := newMockDiscovery()

	// We are testing: Finder A has a block. Finder B notifies Finder A.
	// Finder A examines distances and decides that Finder B is closer to the block.
	// Finder A then pushes the block ID to Finder B via 'Has'.

	blockAddr := "0000000000000000000000000000000000000000000000000000000000000002"

	// Finder A is distance 3 from block (XOR: 2 ^ 1 = 3)
	idA := "0000000000000000000000000000000000000000000000000000000000000001"
	fA, _ := NewMemoryFinder(idA)
	serverA := NewFinderServer(fA, disc)
	tsA := httptest.NewServer(serverA.Handler())
	defer tsA.Close()

	// Finder B is distance 2 from block (XOR: 2 ^ 0 = 2).
	// So B is closer to block than A.
	idB := "0000000000000000000000000000000000000000000000000000000000000000"
	fB, _ := NewMemoryFinder(idB)
	serverB := NewFinderServer(fB, disc)
	tsB := httptest.NewServer(serverB.Handler())
	defer tsB.Close()

	// Register Finder B in discovery so Finder A can push to it
	disc.Register(discovery.ServiceRegistration{
		ID:        idB,
		Address:   tsB.URL,
		Protocols: []string{"finder-v1"},
	})

	// Tell Finder A that storage-1 has the block
	clientA := NewClient(tsA.URL, nil)
	clientA.Has("storage-1", []string{blockAddr})

	// Notify Finder A about Finder B
	fmt.Printf("Notifying A about B\n")
	err := clientA.Notify(idB)
	if err != nil {
		t.Fatalf("Failed to notify: %v", err)
	}

	// Give a moment for the background goroutine to push the block from A to B
	time.Sleep(100 * time.Millisecond)

	// Now ask Finder B if it knows about the block.
	// It should respond that "storage-1" has it.
	clientB := NewClient(tsB.URL, nil)
	res, err := clientB.Find(blockAddr)
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
	tsA := httptest.NewServer(serverA.Handler())
	defer tsA.Close()

	// Add B and C to A's routing table
	idB := "0000000000000000000000000000000000000000000000000000000000000002"
	idC := "0000000000000000000000000000000000000000000000000000000000000003"

	clientA := NewClient(tsA.URL, nil)

	// Register them in discovery (though Notify doesn't strictly need this unless it's pushing blocks)
	disc.Register(discovery.ServiceRegistration{ID: idB, Address: "http://b", Protocols: []string{"finder-v1"}})
	disc.Register(discovery.ServiceRegistration{ID: idC, Address: "http://c", Protocols: []string{"finder-v1"}})

	// Notify A about B and C
	clientA.Notify(idB)
	clientA.Notify(idC)

	// Now ask A for a block it DOES NOT know about. It should return its closest finders.
	blockAddr := "0000000000000000000000000000000000000000000000000000000000000000"

	res, err := clientA.Find(blockAddr)
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
