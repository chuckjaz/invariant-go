package discovery

import (
	"context"
	"testing"
)

// mockParentDiscovery simulates an external network discovery registry.
type mockParentDiscovery struct {
	services map[string]ServiceDescription
}

func newMockParent() *mockParentDiscovery {
	return &mockParentDiscovery{
		services: make(map[string]ServiceDescription),
	}
}

func (m *mockParentDiscovery) Get(ctx context.Context, id string) (ServiceDescription, bool) {
	desc, ok := m.services[id]
	return desc, ok
}

func (m *mockParentDiscovery) Find(ctx context.Context, protocol string, count int) ([]ServiceDescription, error) {
	var results []ServiceDescription
	for _, desc := range m.services {
		for _, p := range desc.Protocols {
			if p == protocol {
				results = append(results, desc)
				if len(results) >= count {
					return results, nil
				}
				break
			}
		}
	}
	return results, nil
}

func (m *mockParentDiscovery) Register(ctx context.Context, reg ServiceRegistration) error {
	m.services[reg.ID] = ServiceDescription{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: reg.Protocols,
	}
	return nil
}

func TestUpstreamDiscovery_Get(t *testing.T) {
	ctx := context.Background()
	local := NewInMemoryDiscovery()
	parent := newMockParent()

	upstream := NewUpstreamDiscovery(local, parent)

	parent.Register(ctx, ServiceRegistration{
		ID:        "parent-1",
		Address:   "1.2.3.4",
		Protocols: []string{"test-proto"},
	})

	// 1. Service not in local but in parent
	desc, ok := upstream.Get(ctx, "parent-1")
	if !ok {
		t.Fatalf("Expected parent-1 to be found in parent discovery")
	}
	if desc.Address != "1.2.3.4" {
		t.Errorf("Expected address 1.2.3.4, got %v", desc.Address)
	}

	// 2. The service should now be cached in local
	localDesc, ok := local.Get(ctx, "parent-1")
	if !ok {
		t.Fatalf("Expected parent-1 to be cached in local discovery")
	}
	if localDesc.Address != "1.2.3.4" {
		t.Errorf("Expected cached address 1.2.3.4, got %v", localDesc.Address)
	}

	// 3. Service registered to upstream directly ONLY goes to local
	err := upstream.Register(ctx, ServiceRegistration{
		ID:        "local-only",
		Address:   "4.3.2.1",
		Protocols: []string{"local-proto"},
	})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	_, parentHasIt := parent.Get(ctx, "local-only")
	if parentHasIt {
		t.Errorf("Expected parent to NOT register local-only service")
	}
}

func TestUpstreamDiscovery_Find(t *testing.T) {
	ctx := context.Background()
	local := NewInMemoryDiscovery()
	parent := newMockParent()

	upstream := NewUpstreamDiscovery(local, parent)

	local.Register(ctx, ServiceRegistration{
		ID:        "node-local",
		Address:   "10.0.0.1",
		Protocols: []string{"http"},
	})

	parent.Register(ctx, ServiceRegistration{
		ID:        "node-parent1",
		Address:   "10.0.0.2",
		Protocols: []string{"http"},
	})

	parent.Register(ctx, ServiceRegistration{
		ID:        "node-parent2",
		Address:   "10.0.0.3",
		Protocols: []string{"http"},
	})

	// 1. Request 2 nodes. 1 should be fulfilled locally, 1 from parent.
	results, err := upstream.Find(ctx, "http", 2)
	if err != nil {
		t.Fatalf("Find failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}

	// Local result should be prioritized
	if results[0].ID != "node-local" {
		t.Errorf("Expected local node to be first, got %s", results[0].ID)
	}

	if results[1].ID != "node-parent1" && results[1].ID != "node-parent2" {
		t.Errorf("Expected parent node appended, got %s", results[1].ID)
	}

	// 2. Verify that the fetched parent node got cached automatically
	foundParentID := results[1].ID
	_, cached := local.Get(ctx, foundParentID)
	if !cached {
		t.Errorf("Expected parent result %s to be cached in local registry", foundParentID)
	}

	// 3. Test deduplication: Registering parent node locally with a different address
	local.Register(ctx, ServiceRegistration{
		ID:        "node-parent2",
		Address:   "local-override",
		Protocols: []string{"http"},
	})

	// Query for 3 nodes, it will pull node-parent1 from parent and NOT duplicate node-parent2
	resultsDedup, err := upstream.Find(ctx, "http", 3)
	if err != nil {
		t.Fatalf("Find failed: %v", err)
	}

	for _, n := range resultsDedup {
		if n.ID == "node-parent2" && n.Address != "local-override" {
			t.Errorf("Local cached value was unexpectedly overwritten or duplicated by parent")
		}
	}
}
