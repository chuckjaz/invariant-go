package discovery

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Assert that InMemoryDiscovery implements Discovery and ControlPlane interfaces
var (
	_ Discovery    = (*InMemoryDiscovery)(nil)
	_ ControlPlane = (*InMemoryDiscovery)(nil)
)

type InMemoryDiscovery struct {
	mu       sync.RWMutex
	services map[string]ServiceRegistration
	clients  map[string]NodeIdentity
	tracker  *HealthTracker
}

func NewInMemoryDiscovery() *InMemoryDiscovery {
	d := &InMemoryDiscovery{
		services: make(map[string]ServiceRegistration),
		clients:  make(map[string]NodeIdentity),
	}
	return d
}

func (d *InMemoryDiscovery) WithHealthTracking(interval, timeout time.Duration) *InMemoryDiscovery {
	if d.tracker != nil {
		d.tracker.Close()
	}
	d.tracker = NewHealthTracker(interval, timeout, d.listAll, d.remove)
	return d
}

func (d *InMemoryDiscovery) Close() error {
	if d.tracker != nil {
		d.tracker.Close()
	}
	return nil
}

func (d *InMemoryDiscovery) listAll() []ServiceRegistration {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var res []ServiceRegistration
	for _, s := range d.services {
		res = append(res, s)
	}
	return res
}

func (d *InMemoryDiscovery) remove(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.services, id)
}

func (d *InMemoryDiscovery) Get(ctx context.Context, id string) (ServiceDescription, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	reg, ok := d.services[id]
	if !ok {
		return ServiceDescription{}, false
	}
	protocolsCopy := make([]string, len(reg.Protocols))
	copy(protocolsCopy, reg.Protocols)
	var tagsCopy []string
	if reg.Tags != nil {
		tagsCopy = make([]string, len(reg.Tags))
		copy(tagsCopy, reg.Tags)
	}
	var idCopy *NodeIdentity
	if reg.Identity != nil {
		idCopy = &NodeIdentity{Name: reg.Identity.Name, Email: reg.Identity.Email}
	}
	return ServiceDescription{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: protocolsCopy,
		Tags:      tagsCopy,
		PublicKey: reg.PublicKey,
		Identity:  idCopy,
	}, true
}

func (d *InMemoryDiscovery) Find(ctx context.Context, protocol, tag string, count int) ([]ServiceDescription, error) {
	d.mu.RLock()
	var results []ServiceDescription
	for _, reg := range d.services {
		hasProtocol := protocol == "" || slices.Contains(reg.Protocols, protocol)
		hasTag := tag == "" || slices.Contains(reg.Tags, tag)

		if hasProtocol && hasTag {
			protocolsCopy := make([]string, len(reg.Protocols))
			copy(protocolsCopy, reg.Protocols)
			var tagsCopy []string
			if reg.Tags != nil {
				tagsCopy = make([]string, len(reg.Tags))
				copy(tagsCopy, reg.Tags)
			}
			var idCopy *NodeIdentity
			if reg.Identity != nil {
				idCopy = &NodeIdentity{Name: reg.Identity.Name, Email: reg.Identity.Email}
			}
			results = append(results, ServiceDescription{
				ID:        reg.ID,
				Address:   reg.Address,
				Protocols: protocolsCopy,
				Tags:      tagsCopy,
				PublicKey: reg.PublicKey,
				Identity:  idCopy,
			})
		}
	}
	d.mu.RUnlock()

	// Sort healthy services first
	if d.tracker != nil {
		d.tracker.Sort(results)
	}

	if count > 0 && len(results) > count {
		results = results[:count]
	}

	return results, nil
}

func (d *InMemoryDiscovery) Register(ctx context.Context, reg ServiceRegistration) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	protocolsCopy := make([]string, len(reg.Protocols))
	copy(protocolsCopy, reg.Protocols)
	var tagsCopy []string
	if reg.Tags != nil {
		tagsCopy = make([]string, len(reg.Tags))
		copy(tagsCopy, reg.Tags)
	}
	var idCopy *NodeIdentity
	if reg.Identity != nil {
		idCopy = &NodeIdentity{Name: reg.Identity.Name, Email: reg.Identity.Email}
	}
	d.services[reg.ID] = ServiceRegistration{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: protocolsCopy,
		Tags:      tagsCopy,
		PublicKey: reg.PublicKey,
		Identity:  idCopy,
	}
	if d.tracker != nil {
		d.tracker.MarkHealthy(reg.ID)
	}
	return nil
}

func (d *InMemoryDiscovery) RegisterClient(ctx context.Context, publicKey string, id NodeIdentity) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.clients[publicKey] = id
	return nil
}

func (d *InMemoryDiscovery) VerifyKey(ctx context.Context, publicKey string) (bool, *NodeIdentity, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if id, ok := d.clients[publicKey]; ok {
		idCopy := id
		return true, &idCopy, nil
	}

	for _, reg := range d.services {
		if reg.PublicKey != "" && reg.PublicKey == publicKey {
			if reg.Identity != nil {
				idCopy := *reg.Identity
				return true, &idCopy, nil
			}
			return true, &NodeIdentity{Name: reg.ID}, nil
		}
	}

	return false, nil, nil
}

func (d *InMemoryDiscovery) AuthorizedKeys(ctx context.Context) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	keySet := make(map[string]struct{})
	for k := range d.clients {
		keySet[k] = struct{}{}
	}
	for _, reg := range d.services {
		if reg.PublicKey != "" {
			keySet[reg.PublicKey] = struct{}{}
		}
	}

	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys, nil
}
