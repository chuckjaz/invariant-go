package discovery

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/chuckjaz/invariant-go/internal/journal"
)

// Assert that FileSystemDiscovery implements Discovery, ControlPlane, ServiceKeyManager, and ServiceKeyRestorer interfaces
var (
	_ Discovery          = (*FileSystemDiscovery)(nil)
	_ ControlPlane       = (*FileSystemDiscovery)(nil)
	_ ServiceKeyManager  = (*FileSystemDiscovery)(nil)
	_ ServiceKeyRestorer = (*FileSystemDiscovery)(nil)
)

type FileSystemDiscovery struct {
	store       *journal.Store[string, ServiceRegistration]
	clientStore *journal.Store[string, NodeIdentity]
	keyStore    *journal.Store[string, ServiceKeyRecord]
	tracker     *HealthTracker
	sink        EventSink
	closeOnce   sync.Once
	closeErr    error
}

func NewFileSystemDiscovery(baseDir string, snapshotInterval time.Duration) (*FileSystemDiscovery, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, err
	}

	store, err := journal.NewStore[string, ServiceRegistration](baseDir, snapshotInterval)
	if err != nil {
		return nil, err
	}

	clientsDir := filepath.Join(baseDir, "clients")
	if err := os.MkdirAll(clientsDir, 0755); err != nil {
		store.Close()
		return nil, err
	}

	clientStore, err := journal.NewStore[string, NodeIdentity](clientsDir, snapshotInterval)
	if err != nil {
		store.Close()
		return nil, err
	}

	keysDir := filepath.Join(baseDir, "service_keys")
	if err := os.MkdirAll(keysDir, 0700); err != nil {
		store.Close()
		clientStore.Close()
		return nil, err
	}

	keyStore, err := journal.NewStore[string, ServiceKeyRecord](keysDir, snapshotInterval)
	if err != nil {
		store.Close()
		clientStore.Close()
		return nil, err
	}

	d := &FileSystemDiscovery{
		store:       store,
		clientStore: clientStore,
		keyStore:    keyStore,
	}

	return d, nil
}

func (d *FileSystemDiscovery) WithHealthTracking(interval, timeout time.Duration) *FileSystemDiscovery {
	if d.tracker != nil {
		d.tracker.Close()
	}

	listFn := func() []ServiceRegistration {
		var res []ServiceRegistration
		d.store.Read(func(m map[string]ServiceRegistration) {
			for _, r := range m {
				res = append(res, r)
			}
		})
		return res
	}

	removeFn := func(id string) {
		d.store.Delete(id, nil)
		if d.sink != nil {
			_ = d.sink.Append(context.Background(), DiscoveryEvent{
				Kind:      EventUnregisterService,
				ServiceID: id,
				Timestamp: time.Now().UTC(),
			})
		}
	}

	d.tracker = NewHealthTracker(interval, timeout, listFn, removeFn)
	return d
}

func (d *FileSystemDiscovery) WithEventSink(sink EventSink) *FileSystemDiscovery {
	d.sink = sink
	return d
}

func (d *FileSystemDiscovery) Close() error {
	d.closeOnce.Do(func() {
		if d.tracker != nil {
			d.tracker.Close()
		}
		if d.clientStore != nil {
			d.clientStore.Close()
		}
		if d.keyStore != nil {
			d.keyStore.Close()
		}
		if d.sink != nil {
			_ = d.sink.Close()
		}
		d.closeErr = d.store.Close()
	})
	return d.closeErr
}

func (d *FileSystemDiscovery) Snapshot() error {
	if d.clientStore != nil {
		if err := d.clientStore.Snapshot(); err != nil {
			return err
		}
	}
	if d.keyStore != nil {
		if err := d.keyStore.Snapshot(); err != nil {
			return err
		}
	}
	return d.store.Snapshot()
}

func (d *FileSystemDiscovery) Get(ctx context.Context, id string) (ServiceDescription, bool) {
	reg, ok := d.store.Get(id)
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

func (d *FileSystemDiscovery) Find(ctx context.Context, protocol, tag string, count int) ([]ServiceDescription, error) {
	var results []ServiceDescription
	d.store.Read(func(store map[string]ServiceRegistration) {
		for _, reg := range store {
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
				if count > 0 && len(results) >= count {
					break
				}
			}
		}
	})

	if d.tracker != nil {
		d.tracker.Sort(results)
	}
	if count > 0 && len(results) > count {
		results = results[:count]
	}

	return results, nil
}

func (d *FileSystemDiscovery) Register(ctx context.Context, reg ServiceRegistration) error {
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
	regCopy := ServiceRegistration{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: protocolsCopy,
		Tags:      tagsCopy,
		PublicKey: reg.PublicKey,
		Identity:  idCopy,
	}

	err := d.store.Put(reg.ID, regCopy, nil)
	if err == nil {
		if d.tracker != nil {
			d.tracker.MarkHealthy(reg.ID)
		}
		if d.sink != nil {
			_ = d.sink.Append(ctx, DiscoveryEvent{
				Kind:      EventRegisterService,
				Service:   &regCopy,
				Timestamp: time.Now().UTC(),
			})
		}
	}
	return err
}

func (d *FileSystemDiscovery) RegisterClient(ctx context.Context, publicKey string, id NodeIdentity) error {
	err := d.clientStore.Put(publicKey, id, nil)
	if err == nil && d.sink != nil {
		_ = d.sink.Append(ctx, DiscoveryEvent{
			Kind:        EventRegisterClient,
			ClientKey:   publicKey,
			ClientIdent: &id,
			Timestamp:   time.Now().UTC(),
		})
	}
	return err
}

func (d *FileSystemDiscovery) VerifyKey(ctx context.Context, publicKey string) (bool, *NodeIdentity, error) {
	if id, ok := d.clientStore.Get(publicKey); ok {
		idCopy := id
		return true, &idCopy, nil
	}

	var found bool
	var identity *NodeIdentity
	d.store.Read(func(m map[string]ServiceRegistration) {
		for _, reg := range m {
			if reg.PublicKey != "" && reg.PublicKey == publicKey {
				found = true
				if reg.Identity != nil {
					idCopy := *reg.Identity
					identity = &idCopy
				} else {
					identity = &NodeIdentity{Name: reg.ID}
				}
				break
			}
		}
	})

	return found, identity, nil
}

func (d *FileSystemDiscovery) AuthorizedKeys(ctx context.Context) ([]string, error) {
	keySet := make(map[string]struct{})
	d.clientStore.Read(func(m map[string]NodeIdentity) {
		for k := range m {
			keySet[k] = struct{}{}
		}
	})
	d.store.Read(func(m map[string]ServiceRegistration) {
		for _, reg := range m {
			if reg.PublicKey != "" {
				keySet[reg.PublicKey] = struct{}{}
			}
		}
	})

	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys, nil
}

func (d *FileSystemDiscovery) CreateServiceKey(ctx context.Context, name string, creator NodeIdentity, expiresAt *time.Time) (*ServiceKeyRecord, error) {
	rec, err := GenerateServiceKey(name, creator, expiresAt)
	if err != nil {
		return nil, err
	}

	if err := d.keyStore.Put(rec.Key, *rec, nil); err != nil {
		return nil, err
	}

	if d.sink != nil {
		_ = d.sink.Append(ctx, DiscoveryEvent{
			Kind:       EventCreateServiceKey,
			ServiceKey: rec,
			Timestamp:  time.Now().UTC(),
		})
	}

	recCopy := *rec
	return &recCopy, nil
}

func (d *FileSystemDiscovery) VerifyServiceKey(ctx context.Context, key string) (bool, *ServiceKeyRecord, error) {
	rec, ok := d.keyStore.Get(key)
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

func (d *FileSystemDiscovery) ListServiceKeys(ctx context.Context) ([]ServiceKeyRecord, error) {
	var records []ServiceKeyRecord
	d.keyStore.Read(func(m map[string]ServiceKeyRecord) {
		for _, r := range m {
			records = append(records, r)
		}
	})
	slices.SortFunc(records, func(a, b ServiceKeyRecord) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return records, nil
}

func (d *FileSystemDiscovery) RevokeServiceKey(ctx context.Context, keyOrID string, revoker NodeIdentity) error {
	var foundKey string
	var rec ServiceKeyRecord
	d.keyStore.Read(func(m map[string]ServiceKeyRecord) {
		for k, r := range m {
			if k == keyOrID || r.ID == keyOrID {
				foundKey = k
				rec = r
				break
			}
		}
	})
	if foundKey == "" {
		return ErrServiceKeyNotFound
	}

	now := time.Now().UTC()
	rec.Revoked = true
	rec.RevokedAt = &now
	rec.RevokedBy = &revoker
	if err := d.keyStore.Put(foundKey, rec, nil); err != nil {
		return err
	}

	if d.sink != nil {
		_ = d.sink.Append(ctx, DiscoveryEvent{
			Kind:      EventRevokeServiceKey,
			RevokeKey: foundKey,
			RevokedBy: &revoker,
			Timestamp: now,
		})
	}
	return nil
}

// RestoreServiceKey directly restores an existing service key record during replay or disaster recovery.
func (d *FileSystemDiscovery) RestoreServiceKey(ctx context.Context, rec ServiceKeyRecord) error {
	return d.keyStore.Put(rec.Key, rec, nil)
}
