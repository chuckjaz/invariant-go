package discovery

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Assert that InMemoryDiscovery implements the Discovery interface
var _ Discovery = (*InMemoryDiscovery)(nil)

type InMemoryDiscovery struct {
	mu       sync.RWMutex
	services map[string]ServiceRegistration
	tracker  *HealthTracker
}

func NewInMemoryDiscovery() *InMemoryDiscovery {
	d := &InMemoryDiscovery{
		services: make(map[string]ServiceRegistration),
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
	return ServiceDescription{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: protocolsCopy,
		Tags:      tagsCopy,
	}, true
}

func (d *InMemoryDiscovery) Find(ctx context.Context, protocol string, count int) ([]ServiceDescription, error) {
	d.mu.RLock()
	var results []ServiceDescription
	for _, reg := range d.services {
		hasProtocol := protocol == "" || slices.Contains(reg.Protocols, protocol)

		if hasProtocol {
			protocolsCopy := make([]string, len(reg.Protocols))
			copy(protocolsCopy, reg.Protocols)
			var tagsCopy []string
			if reg.Tags != nil {
				tagsCopy = make([]string, len(reg.Tags))
				copy(tagsCopy, reg.Tags)
			}
			results = append(results, ServiceDescription{
				ID:        reg.ID,
				Address:   reg.Address,
				Protocols: protocolsCopy,
				Tags:      tagsCopy,
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
	d.services[reg.ID] = ServiceRegistration{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: protocolsCopy,
		Tags:      tagsCopy,
	}
	if d.tracker != nil {
		d.tracker.MarkHealthy(reg.ID)
	}
	return nil
}
