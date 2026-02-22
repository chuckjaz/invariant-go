package distribute

import (
	"encoding/hex"
	"log"
	"slices"
	"sort"
	"sync"
	"time"

	"invariant/internal/discovery"
	"invariant/internal/storage"
)

// InMemoryDistribute is an in-memory implementation of the Distribute interface.
type InMemoryDistribute struct {
	mu        sync.RWMutex
	services  map[string]map[string]struct{} // storage service ID -> set of block addresses
	discovery discovery.Discovery
	repFactor int
}

// NewInMemoryDistribute creates a new InMemoryDistribute instance.
func NewInMemoryDistribute(disc discovery.Discovery, repFactor int) *InMemoryDistribute {
	return &InMemoryDistribute{
		services:  make(map[string]map[string]struct{}),
		discovery: disc,
		repFactor: repFactor,
	}
}

// Register registers a storage service with the distribute service.
func (d *InMemoryDistribute) Register(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.services[id]; !exists {
		d.services[id] = make(map[string]struct{})
	}
	return nil
}

// Has notifies the distribute service that the storage service with the given
// id has the specified data blocks.
func (d *InMemoryDistribute) Has(id string, addresses []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	blocks, exists := d.services[id]
	if !exists {
		blocks = make(map[string]struct{})
		d.services[id] = blocks
	}

	for _, addr := range addresses {
		blocks[addr] = struct{}{}
	}

	return nil
}

// GetBlocks returns all blocks for a given service ID.
func (d *InMemoryDistribute) GetBlocks(id string) []string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	blocks, exists := d.services[id]
	if !exists {
		return nil
	}

	addresses := make([]string, 0, len(blocks))
	for addr := range blocks {
		addresses = append(addresses, addr)
	}
	return addresses
}

// StartSync starts the background synchronization loop.
func (d *InMemoryDistribute) StartSync(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			d.Sync()
		}
	}()
}

// Sync performs a single synchronization pass, ensuring all blocks are replicated to N nodes.
func (d *InMemoryDistribute) Sync() {
	if d.discovery == nil || d.repFactor <= 0 {
		return
	}

	services, err := d.discovery.Find("storage-v1", 1000) // Assumes < 1000 nodes for now
	if err != nil || len(services) == 0 {
		return
	}

	// Build a map of active services
	activeMap := make(map[string]discovery.ServiceDescription)
	d.mu.RLock()
	for _, s := range services {
		// Only consider services that have been explicitly registered
		// (i.e., they exist in the d.services map)
		if _, ok := d.services[s.ID]; ok {
			activeMap[s.ID] = s
		}
	}

	// Build map block -> list of service IDs that contain it
	blockLocations := make(map[string][]string)
	for srvID, blocks := range d.services {
		if _, ok := activeMap[srvID]; !ok {
			// Skip inactive/unreachable services
			continue
		}
		for block := range blocks {
			blockLocations[block] = append(blockLocations[block], srvID)
		}
	}
	d.mu.RUnlock()

	for block, locations := range blockLocations {
		if len(locations) >= d.repFactor {
			continue // Already replicated enough
		}

		// Need to replicate this block
		blockBytes, err := hex.DecodeString(block)
		if err != nil || len(blockBytes) != 32 {
			continue // Invalid block ID
		}

		// Find distance to all active services
		type nodeDist struct {
			id   string
			dist []byte
		}
		var nodes []nodeDist
		for srvID := range activeMap {
			srvBytes, err := hex.DecodeString(srvID)
			if err != nil || len(srvBytes) != 32 {
				continue // Invalid service ID
			}
			nodes = append(nodes, nodeDist{
				id:   srvID,
				dist: Distance(blockBytes, srvBytes),
			})
		}

		// Sort by distance (closest first)
		sort.Slice(nodes, func(i, j int) bool {
			return CmpDistance(nodes[i].dist, nodes[j].dist) < 0
		})

		// Pick destination nodes that don't have the block
		hasBlock := func(id string) bool {
			return slices.Contains(locations, id)
		}

		sourceSrvID := locations[0]
		sourceSrv := activeMap[sourceSrvID]

		needed := d.repFactor - len(locations)
		for _, node := range nodes {
			if needed <= 0 {
				break
			}
			if !hasBlock(node.id) {
				destSrv := activeMap[node.id]
				// Create store client from destSrv URL
				// And tell dest to fetch from source via its ID so dest looks it up in discovery
				c := storage.NewClient(destSrv.Address, nil)
				err := c.Fetch(block, sourceSrv.ID)
				if err != nil {
					log.Printf("Failed to sync block %s to %s via fetch: %v", block, destSrv.Address, err)

					// Fallback: try using get and put directly
					sourceClient := storage.NewClient(sourceSrv.Address, nil)
					if data, ok := sourceClient.Get(block); ok {
						success, errStore := c.StoreAt(block, data)
						data.Close()
						if errStore == nil && success {
							log.Printf("Fallback synced block %s from %s to %s via direct relay", block, sourceSrv.Address, destSrv.Address)
							needed--
						} else {
							log.Printf("Failed to relay block %s to %s: %v", block, destSrv.Address, errStore)
						}
					} else {
						log.Printf("Failed to retrieve block %s from %s for fallback relay", block, sourceSrv.Address)
					}
				} else {
					log.Printf("Synced block %s from %s to %s", block, sourceSrv.Address, destSrv.Address)
					needed--
				}
			}
		}
	}
}
