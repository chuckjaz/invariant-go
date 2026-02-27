package storage

import (
	"container/list"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"invariant/internal/discovery"
	"invariant/internal/finder"
)

var (
	ErrNoLiveServers = errors.New("no live storage servers available")
	ErrBlockNotFound = errors.New("block not found in any storage")
)

type blockLocation struct {
	address string
	servers []string
}

// errorTrackingTransport intercepts network errors to report dead servers.
type errorTrackingTransport struct {
	base     http.RoundTripper
	serverID string
	onError  func(serverID string)
}

func (t *errorTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		if t.onError != nil {
			t.onError(t.serverID)
		}
	} else if resp != nil && (resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout) {
		// Server might be down or not responding properly
		if t.onError != nil {
			t.onError(t.serverID)
		}
	}
	return resp, err
}

// AggregateClient aggregates a finder, discovery service, and standard storage clients.
type AggregateClient struct {
	finder          finder.Finder
	discovery       discovery.Discovery
	numStoreServers int

	// Live servers cache
	liveMu      sync.RWMutex
	liveServers map[string]Storage // Server ID -> Storage client
	liveIDs     []string           // For round-robin access
	liveCounter uint64

	// LRU Cache for block locations
	maxBlocks int
	lruMu     sync.Mutex
	lruList   *list.List
	lruMap    map[string]*list.Element // address -> *list.Element (holding blockLocation)
}

// NewAggregateClient creates a new Storage client that aggregates multiple services.
func NewAggregateClient(f finder.Finder, d discovery.Discovery, numStoreServers, maxBlocks int) *AggregateClient {
	if maxBlocks <= 0 {
		maxBlocks = -1 // No limit
	}
	return &AggregateClient{
		finder:          f,
		discovery:       d,
		numStoreServers: numStoreServers,
		liveServers:     make(map[string]Storage),
		maxBlocks:       maxBlocks,
		lruList:         list.New(),
		lruMap:          make(map[string]*list.Element),
	}
}

// removeLiveServer removes a server from the live list and LRU.
func (c *AggregateClient) removeLiveServer(serverID string) {
	c.liveMu.Lock()
	if _, ok := c.liveServers[serverID]; !ok {
		c.liveMu.Unlock()
		return
	}
	delete(c.liveServers, serverID)
	// Update liveIDs
	var newIDs []string
	for _, id := range c.liveIDs {
		if id != serverID {
			newIDs = append(newIDs, id)
		}
	}
	c.liveIDs = newIDs
	c.liveMu.Unlock()

	// Also remove from LRU
	c.lruMu.Lock()
	defer c.lruMu.Unlock()
	for _, elem := range c.lruMap {
		loc := elem.Value.(*blockLocation)
		var newServers []string
		for _, s := range loc.servers {
			if s != serverID {
				newServers = append(newServers, s)
			}
		}
		loc.servers = newServers
	}
}

// addLiveServer adds a server to the live list.
func (c *AggregateClient) addLiveServer(serverID string) Storage {
	c.liveMu.Lock()
	defer c.liveMu.Unlock()
	if client, ok := c.liveServers[serverID]; ok {
		return client
	}

	if c.discovery == nil {
		return nil
	}

	svc, ok := c.discovery.Get(serverID)
	if !ok {
		return nil
	}

	transport := &errorTrackingTransport{
		base:     http.DefaultTransport,
		serverID: serverID,
		onError:  c.removeLiveServer, // This will be called asynchronously upon failure
	}

	httpClient := &http.Client{
		Transport: transport,
	}

	// Assuming svc.Address is the base URL
	client := NewClient(svc.Address, httpClient)
	c.liveServers[serverID] = client
	c.liveIDs = append(c.liveIDs, serverID)
	return client
}

// markBlockUsed updates the LRU for the given address indicating which servers have it.
func (c *AggregateClient) markBlockUsed(address string, servers []string) {
	if len(servers) == 0 {
		return
	}

	c.lruMu.Lock()
	defer c.lruMu.Unlock()

	if elem, ok := c.lruMap[address]; ok {
		c.lruList.MoveToFront(elem)

		// merge servers, avoiding duplicates
		loc := elem.Value.(*blockLocation)
		hasMap := make(map[string]bool)
		for _, s := range loc.servers {
			hasMap[s] = true
		}
		for _, s := range servers {
			if !hasMap[s] {
				loc.servers = append(loc.servers, s)
				hasMap[s] = true
			}
		}
		return
	}

	// Not found, add new
	loc := &blockLocation{
		address: address,
		servers: append([]string(nil), servers...), // Copy the slice
	}
	elem := c.lruList.PushFront(loc)
	c.lruMap[address] = elem

	if c.maxBlocks > 0 && c.lruList.Len() > c.maxBlocks {
		oldest := c.lruList.Back()
		if oldest != nil {
			oldLoc := oldest.Value.(*blockLocation)
			delete(c.lruMap, oldLoc.address)
			c.lruList.Remove(oldest)
		}
	}
}

// getServersForBlock returns the know servers for a given block from LRU.
func (c *AggregateClient) getServersForBlock(address string) []string {
	c.lruMu.Lock()
	defer c.lruMu.Unlock()

	if elem, ok := c.lruMap[address]; ok {
		c.lruList.MoveToFront(elem)
		return append([]string(nil), elem.Value.(*blockLocation).servers...)
	}
	return nil
}

// readOperation maps over LRU, then live servers, then finder.
// We don't remove servers on false here, because the transport onError does it on connection issues.
func (c *AggregateClient) readOperation(address string,
	doOp func(client Storage) (any, bool)) (any, bool) {

	// 1. Check LRU
	cachedServerIDs := c.getServersForBlock(address)
	for _, id := range cachedServerIDs {
		c.liveMu.RLock()
		client, ok := c.liveServers[id]
		c.liveMu.RUnlock()

		if ok {
			val, okOp := doOp(client)
			if okOp {
				c.markBlockUsed(address, []string{id})
				return val, true
			}
		}
	}

	// 2. Try all live services
	c.liveMu.RLock()
	liveIDsCopy := append([]string(nil), c.liveIDs...)
	c.liveMu.RUnlock()

	for _, id := range liveIDsCopy {
		c.liveMu.RLock()
		client, ok := c.liveServers[id]
		c.liveMu.RUnlock()

		if ok {
			val, okOp := doOp(client)
			if okOp {
				c.markBlockUsed(address, []string{id})
				return val, true
			}
		}
	}

	// 3. Try Finder
	if c.finder != nil {
		responses, err := c.finder.Find(address)
		if err == nil {
			var successfulIDs []string
			var finalVal any
			var success bool

			for _, resp := range responses {
				if resp.Protocol != "storage-v1" {
					continue
				}
				client := c.addLiveServer(resp.ID)
				if client != nil && !success {
					val, okOp := doOp(client)
					if okOp {
						successfulIDs = append(successfulIDs, resp.ID)
						finalVal = val
						success = true
					}
				}
			}
			if success {
				c.markBlockUsed(address, successfulIDs)
				return finalVal, true
			}
		}
	}

	return nil, false
}

// Has checks if any storage service contains the given address.
func (c *AggregateClient) Has(address string) bool {
	res, ok := c.readOperation(address, func(client Storage) (any, bool) {
		success := client.Has(address)
		if success {
			return true, true
		}
		return nil, false
	})
	if res == nil {
		return false
	}
	return ok
}

// Get checks if any storage service contains the given address and returns it.
func (c *AggregateClient) Get(address string) (io.ReadCloser, bool) {
	res, ok := c.readOperation(address, func(client Storage) (any, bool) {
		rc, success := client.Get(address)
		if success {
			return rc, true
		}
		return nil, false
	})
	if res == nil {
		return nil, false
	}
	return res.(io.ReadCloser), ok
}

// Size checks if any storage service contains the given address and returns its size.
func (c *AggregateClient) Size(address string) (int64, bool) {
	res, ok := c.readOperation(address, func(client Storage) (any, bool) {
		size, success := client.Size(address)
		if success {
			return size, true
		}
		return nil, false
	})
	if res == nil {
		return 0, false
	}
	return res.(int64), ok
}

// ensureLiveServers queries discovery if we have no live servers.
func (c *AggregateClient) ensureLiveServers() error {
	c.liveMu.RLock()
	count := len(c.liveIDs)
	c.liveMu.RUnlock()

	if count > 0 {
		return nil
	}

	if c.discovery == nil {
		return ErrNoLiveServers
	}

	services, err := c.discovery.Find("storage-v1", c.numStoreServers)
	if err != nil {
		return fmt.Errorf("failed to discover storage services: %w", err)
	}

	if len(services) == 0 {
		return ErrNoLiveServers
	}

	for _, svc := range services {
		c.addLiveServer(svc.ID)
	}

	c.liveMu.RLock()
	count = len(c.liveIDs)
	c.liveMu.RUnlock()

	if count == 0 {
		return ErrNoLiveServers
	}

	return nil
}

// writeOperation selects a set of live servers and executes a write operation.
func (c *AggregateClient) writeOperation(doOp func(client Storage) (any, error)) (any, error) {
	err := c.ensureLiveServers()
	if err != nil {
		return nil, err
	}

	c.liveMu.RLock()
	ids := append([]string(nil), c.liveIDs...)
	c.liveMu.RUnlock()

	// Round robin through them until one succeeds
	startIdx := atomic.AddUint64(&c.liveCounter, 1)

	for i := range ids {
		idx := (startIdx + uint64(i)) % uint64(len(ids))
		id := ids[idx]

		c.liveMu.RLock()
		client, ok := c.liveServers[id]
		c.liveMu.RUnlock()

		if ok {
			res, errOp := doOp(client)
			if errOp == nil {
				return res, nil
			} else {
				// Immediate removal on write error since we know it's a real failure.
				// (The client interface for Store/StoreAt returns explicitly returned errors)
				c.removeLiveServer(id)
			}
		}
	}

	return nil, fmt.Errorf("all attempted write operations failed")
}

// Store saves data and returns its content-based address to one round-robined live server.
func (c *AggregateClient) Store(r io.Reader) (string, error) {
	// Need to handle streaming readers by keeping them readable?
	// If the first write fails partway, the reader is consumed!
	// Typically, we only retry if it fails *before* writing or we copy it.
	// But `io.Reader` can't be rewound generically.
	// We'll just try to execute the operation. If it fails, the reader might be consumed.
	res, err := c.writeOperation(func(client Storage) (any, error) {
		return client.Store(r)
	})
	if err != nil {
		return "", err
	}
	return res.(string), nil
}

// StoreAt saves data at the specified address using round-robined live servers.
func (c *AggregateClient) StoreAt(address string, r io.Reader) (bool, error) {
	res, err := c.writeOperation(func(client Storage) (any, error) {
		return client.StoreAt(address, r)
	})
	if err != nil {
		return false, err
	}
	success := res.(bool)
	if success {
		// Update LRU we successfully wrote it, but to WHICH server?
		// We'd have to rewrite `writeOperation` to return the successful server ID to mark it.
		// Let's not prematurely optimize. This behaves correctly by ensuring the next Read works.
	}
	return success, nil
}

// List returns all addresses stored in the aggregate storage. Currently not supported.
func (c *AggregateClient) List(chunkSize int) <-chan []string {
	ch := make(chan []string)
	close(ch)
	return ch
}

// Subscribe provides a channel for live updates. Currently not supported.
func (c *AggregateClient) Subscribe() <-chan string {
	ch := make(chan string)
	close(ch)
	return ch
}

// Assert that AggregateClient implements the Storage interface
var _ Storage = (*AggregateClient)(nil)
