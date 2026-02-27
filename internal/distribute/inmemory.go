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

type nodeState struct {
	blocks   map[string]struct{}
	desc     *discovery.ServiceDescription
	failures int
}

// InMemoryDistribute is an in-memory implementation of the Distribute interface.
type InMemoryDistribute struct {
	mu          sync.RWMutex
	services    map[string]*nodeState // storage service ID -> state
	discovery   discovery.Discovery
	repFactor   int
	maxAttempts int
}

// NewInMemoryDistribute creates a new InMemoryDistribute instance.
func NewInMemoryDistribute(disc discovery.Discovery, repFactor int, maxAttempts int) *InMemoryDistribute {
	return &InMemoryDistribute{
		services:    make(map[string]*nodeState),
		discovery:   disc,
		repFactor:   repFactor,
		maxAttempts: maxAttempts,
	}
}

// Register registers a storage service with the distribute service.
func (d *InMemoryDistribute) Register(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.services[id]; !exists {
		d.services[id] = &nodeState{
			blocks: make(map[string]struct{}),
		}
	}
	return nil
}

// Has notifies the distribute service that the storage service with the given
// id has the specified data blocks.
func (d *InMemoryDistribute) Has(id string, addresses []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	state, exists := d.services[id]
	if !exists {
		state = &nodeState{
			blocks: make(map[string]struct{}),
		}
		d.services[id] = state
	}

	for _, addr := range addresses {
		state.blocks[addr] = struct{}{}
	}

	return nil
}

// GetBlocks returns all blocks for a given service ID.
func (d *InMemoryDistribute) GetBlocks(id string) []string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	state, exists := d.services[id]
	if !exists {
		return nil
	}

	addresses := make([]string, 0, len(state.blocks))
	for addr := range state.blocks {
		addresses = append(addresses, addr)
	}
	return addresses
}

// getServiceAddress attempts to get the service address for an ID, using cache if
// available, or making a fresh request to the discovery service if required.
func (d *InMemoryDistribute) getServiceAddress(id string, forceRefresh bool) (string, bool) {
	d.mu.RLock()
	state, exists := d.services[id]
	d.mu.RUnlock()

	if !exists {
		return "", false
	}

	if !forceRefresh {
		d.mu.RLock()
		if state.desc != nil {
			addr := state.desc.Address
			d.mu.RUnlock()
			return addr, true
		}
		d.mu.RUnlock()
	}

	// Fetch from discovery
	desc, ok := d.discovery.Get(id)
	if !ok {
		return "", false
	}

	d.mu.Lock()
	// re-check existence in case it was deleted
	if state, stillExists := d.services[id]; stillExists {
		state.desc = &desc
	}
	d.mu.Unlock()

	return desc.Address, true
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

	// Build map block -> list of service IDs that contain it
	blockLocations := make(map[string][]string)
	d.mu.RLock()
	for srvID, state := range d.services {
		for block := range state.blocks {
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

		// Find distance to all registered services
		type nodeDist struct {
			id   string
			dist []byte
		}
		var nodes []nodeDist
		d.mu.RLock()
		for srvID := range d.services {
			srvBytes, err := hex.DecodeString(srvID)
			if err != nil || len(srvBytes) != 32 {
				continue // Invalid service ID
			}
			nodes = append(nodes, nodeDist{
				id:   srvID,
				dist: Distance(blockBytes, srvBytes),
			})
		}
		d.mu.RUnlock()

		// Sort by distance (closest first)
		sort.Slice(nodes, func(i, j int) bool {
			return CmpDistance(nodes[i].dist, nodes[j].dist) < 0
		})

		// Pick destination nodes that don't have the block
		hasBlock := func(id string) bool {
			return slices.Contains(locations, id)
		}

		sourceSrvID := locations[0]
		sourceAddr, ok := d.getServiceAddress(sourceSrvID, false)
		if !ok {
			log.Printf("Failed to resolve address for source node %s", sourceSrvID)
			continue
		}

		needed := d.repFactor - len(locations)
		for _, node := range nodes {
			if needed <= 0 {
				break
			}
			if !hasBlock(node.id) {
				destSrvID := node.id

				// Try to replicate to this node, with retries on failure
				success := false
				for attempt := range 2 {
					forceRefresh := attempt > 0 // Force refresh on retry
					destAddr, ok := d.getServiceAddress(destSrvID, forceRefresh)
					if !ok {
						log.Printf("Failed to resolve address for destination node %s", destSrvID)
						break // Can't resolve, give up on this node for now
					}

					if attempt > 0 {
						// On retry, we also want to be sure our source address is still good
						newSourceAddr, ok := d.getServiceAddress(sourceSrvID, true)
						if ok {
							sourceAddr = newSourceAddr
						} else {
							break // Source vanished
						}
					}

					// Create store client from destSrv URL
					// And tell dest to fetch from source via its ID so dest looks it up in discovery
					c := storage.NewClient(destAddr, nil)
					err := c.Fetch(block, sourceSrvID)
					if err != nil {
						// Fallback: try using get and put directly
						sourceClient := storage.NewClient(sourceAddr, nil)
						if data, ok := sourceClient.Get(block); ok {
							storeSuccess, errStore := c.StoreAt(block, data)
							data.Close()
							if errStore == nil && storeSuccess {
								log.Printf("Fallback synced block %s from %s to %s via direct relay", block, sourceAddr, destAddr)
								success = true
								break // success
							}
						}
					} else {
						log.Printf("Synced block %s from %s to %s via fetch", block, sourceAddr, destAddr)
						success = true
						break // success
					}
					log.Printf("Attempt %d failed to sync block %s to %s", attempt+1, block, destAddr)
				}

				if success {
					needed--
					d.mu.Lock()
					if state, ok := d.services[destSrvID]; ok {
						state.failures = 0
					}
					d.mu.Unlock()
				} else {
					d.mu.Lock()
					if state, ok := d.services[destSrvID]; ok {
						state.failures++
						if state.failures >= d.maxAttempts {
							log.Printf("Removing node %s due to max failures (%d)", destSrvID, state.failures)
							delete(d.services, destSrvID)
						}
					}
					d.mu.Unlock()
				}
			}
		}
	}
}
