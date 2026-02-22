package finder

import (
	"fmt"
	"sort"
	"sync"
)

// FindResponse represents a service holding or knowing about a block.
type FindResponse struct {
	ID       string `json:"id"`
	Protocol string `json:"protocol"`
}

// Finder defines the interface for the Kademlia-based finder service.
type Finder interface {
	ID() string
	Find(address string) ([]FindResponse, error)
	Has(storageID string, addresses []string) error
	Notify(finderID string) error
}

// FinderTest provides testing and diagnostic methods.
type FinderTest interface {
	SnapshotBlocks() map[string][]string
	RoutingTable() *RoutingTable
}

// MemoryFinder provides an in-memory implementation of the Finder interface.
// It uses Kademlia concepts for discovering and storing knowledge of block locations.
type MemoryFinder struct {
	id           NodeID
	idStr        string
	routingTable *RoutingTable

	// mu protects the knownBlocks map
	mu          sync.RWMutex
	knownBlocks map[string]map[string]struct{} // blockAddress -> set of storage IDs
}

// NewMemoryFinder creates a new MemoryFinder instance.
func NewMemoryFinder(idStr string) (*MemoryFinder, error) {
	nodeID, err := ParseNodeID(idStr)
	if err != nil {
		return nil, fmt.Errorf("invalid finder ID: %w", err)
	}

	return &MemoryFinder{
		id:           nodeID,
		idStr:        idStr,
		routingTable: NewRoutingTable(nodeID),
		knownBlocks:  make(map[string]map[string]struct{}),
	}, nil
}

// ID returns the ID of this finder service.
func (f *MemoryFinder) ID() string {
	return f.idStr
}

// Find looks up a block address. First, it checks if any known storage
// nodes have it. If so, it returns them. Otherwise, it returns the k-closest
// finder nodes to the address from its routing table.
func (f *MemoryFinder) Find(address string) ([]FindResponse, error) {
	f.mu.RLock()
	storages, ok := f.knownBlocks[address]
	f.mu.RUnlock()

	var responses []FindResponse

	if ok && len(storages) > 0 {
		// Return the storage services that have the block
		// Sort the output for stable testing
		var sorted []string
		for sID := range storages {
			sorted = append(sorted, sID)
		}
		sort.Strings(sorted)

		for _, sID := range sorted {
			responses = append(responses, FindResponse{
				ID:       sID,
				Protocol: "storage-v1",
			})
		}
		return responses, nil
	}

	// Not found locally. Use Kademlia routing to find the closest finders.
	targetID, err := ParseNodeID(address)
	if err != nil {
		return nil, fmt.Errorf("invalid block address format: %w", err)
	}

	closestFinders := f.routingTable.FindClosest(targetID, BucketSize)
	for _, n := range closestFinders {
		responses = append(responses, FindResponse{
			ID:       n.String(),
			Protocol: "finder-v1",
		})
	}

	return responses, nil
}

// Has registers that a storage ID holds the given blocks.
func (f *MemoryFinder) Has(storageID string, addresses []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, addr := range addresses {
		if f.knownBlocks[addr] == nil {
			f.knownBlocks[addr] = make(map[string]struct{})
		}
		f.knownBlocks[addr][storageID] = struct{}{}
	}

	return nil
}

// Notify is called when another finder notifies us of their existence.
func (f *MemoryFinder) Notify(finderID string) error {
	nodeID, err := ParseNodeID(finderID)
	if err != nil {
		return fmt.Errorf("invalid finder ID in notify: %w", err)
	}
	f.routingTable.Add(nodeID)
	return nil
}

// RoutingTable returns the finder's routing table.
func (f *MemoryFinder) RoutingTable() *RoutingTable {
	return f.routingTable
}

// SnapshotBlocks returns a map of all known blocks and the storage nodes that have them.
func (f *MemoryFinder) SnapshotBlocks() map[string][]string {
	f.mu.RLock()
	defer f.mu.RUnlock()

	snap := make(map[string][]string)
	for addr, storages := range f.knownBlocks {
		var sList []string
		for s := range storages {
			sList = append(sList, s)
		}
		sort.Strings(sList) // Stable ordering for deterministic output
		snap[addr] = sList
	}
	return snap
}
