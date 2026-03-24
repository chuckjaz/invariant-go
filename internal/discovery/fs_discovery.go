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
	store *journal.Store[string, ServiceRegistration]
}

func NewFileSystemDiscovery(baseDir string, snapshotInterval time.Duration) (*FileSystemDiscovery, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, err
	}

	store, err := journal.NewStore[string, ServiceRegistration](baseDir, snapshotInterval)
	if err != nil {
		return nil, err
	}

	return &FileSystemDiscovery{
		store: store,
	}, nil
}

func (d *FileSystemDiscovery) Close() error {
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
			if protocol == "" {
				continue
			}

			if slices.Contains(reg.Protocols, protocol) {
				protocolsCopy := make([]string, len(reg.Protocols))
				copy(protocolsCopy, reg.Protocols)
				results = append(results, ServiceDescription{
					ID:        reg.ID,
					Address:   reg.Address,
					Protocols: protocolsCopy,
				})
				if len(results) >= count {
					break
				}
			}
		}
	})

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

	return d.store.Put(reg.ID, regCopy, nil)
}
