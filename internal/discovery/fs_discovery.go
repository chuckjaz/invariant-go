package discovery

import (
	"context"
	"os"
	"slices"
	"time"

	"invariant/internal/journal"
)

// Assert that FileSystemDiscovery implements the Discovery interface
var _ Discovery = (*FileSystemDiscovery)(nil)

type FileSystemDiscovery struct {
	store   *journal.Store[string, ServiceRegistration]
	tracker *HealthTracker
}

func NewFileSystemDiscovery(baseDir string, snapshotInterval time.Duration) (*FileSystemDiscovery, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, err
	}

	store, err := journal.NewStore[string, ServiceRegistration](baseDir, snapshotInterval)
	if err != nil {
		return nil, err
	}

	d := &FileSystemDiscovery{
		store: store,
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
	}

	d.tracker = NewHealthTracker(interval, timeout, listFn, removeFn)
	return d
}

func (d *FileSystemDiscovery) Close() error {
	if d.tracker != nil {
		d.tracker.Close()
	}
	return d.store.Close()
}

func (d *FileSystemDiscovery) Get(ctx context.Context, id string) (ServiceDescription, bool) {
	reg, ok := d.store.Get(id)
	if !ok {
		return ServiceDescription{}, false
	}

	protocolsCopy := make([]string, len(reg.Protocols))
	copy(protocolsCopy, reg.Protocols)

	return ServiceDescription{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: protocolsCopy,
	}, true
}

func (d *FileSystemDiscovery) Find(ctx context.Context, protocol string, count int) ([]ServiceDescription, error) {
	var results []ServiceDescription
	d.store.Read(func(store map[string]ServiceRegistration) {
		for _, reg := range store {
			if protocol == "" || slices.Contains(reg.Protocols, protocol) {
				protocolsCopy := make([]string, len(reg.Protocols))
				copy(protocolsCopy, reg.Protocols)
				results = append(results, ServiceDescription{
					ID:        reg.ID,
					Address:   reg.Address,
					Protocols: protocolsCopy,
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
	regCopy := ServiceRegistration{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: protocolsCopy,
	}

	err := d.store.Put(reg.ID, regCopy, nil)
	if err == nil && d.tracker != nil {
		d.tracker.MarkHealthy(reg.ID)
	}
	return err
}
