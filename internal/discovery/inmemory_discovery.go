package discovery

import "slices"

import "sync"

// Assert that InMemoryDiscovery implements the Discovery interface
var _ Discovery = (*InMemoryDiscovery)(nil)

type InMemoryDiscovery struct {
	mu       sync.RWMutex
	services map[string]ServiceRegistration
}

func NewInMemoryDiscovery() *InMemoryDiscovery {
	return &InMemoryDiscovery{
		services: make(map[string]ServiceRegistration),
	}
}

func (d *InMemoryDiscovery) Get(id string) (ServiceDescription, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	reg, ok := d.services[id]
	if !ok {
		return ServiceDescription{}, false
	}
	return ServiceDescription{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: reg.Protocols,
	}, true
}

func (d *InMemoryDiscovery) Find(protocol string, count int) ([]ServiceDescription, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var results []ServiceDescription
	for _, reg := range d.services {
		if protocol == "" {
			continue
		}

		hasProtocol := slices.Contains(reg.Protocols, protocol)

		if hasProtocol {
			results = append(results, ServiceDescription{
				ID:        reg.ID,
				Address:   reg.Address,
				Protocols: reg.Protocols,
			})
			if len(results) >= count {
				break
			}
		}
	}

	return results, nil
}

func (d *InMemoryDiscovery) Register(reg ServiceRegistration) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.services[reg.ID] = reg
	return nil
}
