package discovery

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Assert that InMemoryDiscovery implements Discovery, ControlPlane, ServiceKeyManager, and ServiceKeyRestorer interfaces
var (
	_ Discovery          = (*InMemoryDiscovery)(nil)
	_ ControlPlane       = (*InMemoryDiscovery)(nil)
	_ ServiceKeyManager  = (*InMemoryDiscovery)(nil)
	_ ServiceKeyRestorer = (*InMemoryDiscovery)(nil)
)

type InMemoryDiscovery struct {
	mu          sync.RWMutex
	services    map[string]ServiceRegistration
	clients     map[string]NodeIdentity
	serviceKeys map[string]ServiceKeyRecord
	tracker     *HealthTracker
	sink        EventSink
}

func NewInMemoryDiscovery() *InMemoryDiscovery {
	d := &InMemoryDiscovery{
		services:    make(map[string]ServiceRegistration),
		clients:     make(map[string]NodeIdentity),
		serviceKeys: make(map[string]ServiceKeyRecord),
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

func (d *InMemoryDiscovery) WithEventSink(sink EventSink) *InMemoryDiscovery {
	d.sink = sink
	return d
}

func (d *InMemoryDiscovery) Close() error {
	if d.tracker != nil {
		d.tracker.Close()
	}
	if d.sink != nil {
		_ = d.sink.Close()
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
	delete(d.services, id)
	sink := d.sink
	d.mu.Unlock()

	if sink != nil {
		_ = sink.Append(context.Background(), DiscoveryEvent{
			Kind:      EventUnregisterService,
			ServiceID: id,
			Timestamp: time.Now().UTC(),
		})
	}
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
	savedReg := ServiceRegistration{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: protocolsCopy,
		Tags:      tagsCopy,
		PublicKey: reg.PublicKey,
		Identity:  idCopy,
	}

	d.mu.Lock()
	d.services[reg.ID] = savedReg
	tracker := d.tracker
	sink := d.sink
	d.mu.Unlock()

	if tracker != nil {
		tracker.MarkHealthy(reg.ID)
	}
	if sink != nil {
		_ = sink.Append(ctx, DiscoveryEvent{
			Kind:      EventRegisterService,
			Service:   &savedReg,
			Timestamp: time.Now().UTC(),
		})
	}
	return nil
}

func (d *InMemoryDiscovery) RegisterClient(ctx context.Context, publicKey string, id NodeIdentity) error {
	d.mu.Lock()
	d.clients[publicKey] = id
	sink := d.sink
	d.mu.Unlock()

	if sink != nil {
		_ = sink.Append(ctx, DiscoveryEvent{
			Kind:        EventRegisterClient,
			ClientKey:   publicKey,
			ClientIdent: &id,
			Timestamp:   time.Now().UTC(),
		})
	}
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

func (d *InMemoryDiscovery) CreateServiceKey(ctx context.Context, name string, creator NodeIdentity, expiresAt *time.Time) (*ServiceKeyRecord, error) {
	rec, err := GenerateServiceKey(name, creator, expiresAt)
	if err != nil {
		return nil, err
	}

	d.mu.Lock()
	d.serviceKeys[rec.Key] = *rec
	sink := d.sink
	d.mu.Unlock()

	if sink != nil {
		_ = sink.Append(ctx, DiscoveryEvent{
			Kind:       EventCreateServiceKey,
			ServiceKey: rec,
			Timestamp:  time.Now().UTC(),
		})
	}

	recCopy := *rec
	return &recCopy, nil
}

func (d *InMemoryDiscovery) VerifyServiceKey(ctx context.Context, key string) (bool, *ServiceKeyRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rec, ok := d.serviceKeys[key]
	if !ok {
		return false, nil, ErrServiceKeyNotFound
	}
	if rec.Revoked {
		return false, nil, ErrServiceKeyRevoked
	}
	if rec.ExpiresAt != nil && time.Now().After(*rec.ExpiresAt) {
		return false, nil, ErrServiceKeyExpired
	}
	recCopy := rec
	return true, &recCopy, nil
}

func (d *InMemoryDiscovery) ListServiceKeys(ctx context.Context) ([]ServiceKeyRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	records := make([]ServiceKeyRecord, 0, len(d.serviceKeys))
	for _, rec := range d.serviceKeys {
		records = append(records, rec)
	}
	slices.SortFunc(records, func(a, b ServiceKeyRecord) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return records, nil
}

func (d *InMemoryDiscovery) RevokeServiceKey(ctx context.Context, keyOrID string, revoker NodeIdentity) error {
	d.mu.Lock()
	var foundKey string
	var rec ServiceKeyRecord
	for k, r := range d.serviceKeys {
		if k == keyOrID || r.ID == keyOrID {
			foundKey = k
			rec = r
			break
		}
	}
	if foundKey == "" {
		d.mu.Unlock()
		return ErrServiceKeyNotFound
	}

	now := time.Now().UTC()
	rec.Revoked = true
	rec.RevokedAt = &now
	rec.RevokedBy = &revoker
	d.serviceKeys[foundKey] = rec
	sink := d.sink
	d.mu.Unlock()

	if sink != nil {
		_ = sink.Append(ctx, DiscoveryEvent{
			Kind:      EventRevokeServiceKey,
			RevokeKey: foundKey,
			RevokedBy: &revoker,
			Timestamp: now,
		})
	}
	return nil
}

// RestoreServiceKey directly restores an existing service key record during replay or disaster recovery.
func (d *InMemoryDiscovery) RestoreServiceKey(ctx context.Context, rec ServiceKeyRecord) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.serviceKeys[rec.Key] = rec
	return nil
}
