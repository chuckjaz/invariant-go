package storage

import (
	"context"
	"io"
)

// JoinedStorage combines two storages: a primary and a secondary.
// Writes always go to the primary storage.
// Reads (Get, Has, Size) try the primary storage first, and fall back to the secondary if the block is not found.
type JoinedStorage struct {
	primary   Storage
	secondary Storage
}

// NewJoinedStorage creates a new JoinedStorage.
func NewJoinedStorage(primary, secondary Storage) *JoinedStorage {
	return &JoinedStorage{
		primary:   primary,
		secondary: secondary,
	}
}

// Has checks if the block exists in primary, then secondary.
func (j *JoinedStorage) Has(ctx context.Context, address string) bool {
	if j.primary.Has(ctx, address) {
		return true
	}
	if j.secondary != nil {
		return j.secondary.Has(ctx, address)
	}
	return false
}

// Get fetches the block from primary, then secondary.
func (j *JoinedStorage) Get(ctx context.Context, address string) (io.ReadCloser, bool) {
	rc, ok := j.primary.Get(ctx, address)
	if ok {
		return rc, true
	}
	if j.secondary != nil {
		return j.secondary.Get(ctx, address)
	}
	return nil, false
}

// Store writes the reader to the primary storage.
func (j *JoinedStorage) Store(ctx context.Context, r io.Reader) (string, error) {
	return j.primary.Store(ctx, r)
}

// StoreAt writes the reader to the primary storage at the specific address.
func (j *JoinedStorage) StoreAt(ctx context.Context, address string, r io.Reader) (bool, error) {
	return j.primary.StoreAt(ctx, address, r)
}

// Size returns the size of the block from primary, then secondary.
func (j *JoinedStorage) Size(ctx context.Context, address string) (int64, bool) {
	size, ok := j.primary.Size(ctx, address)
	if ok {
		return size, true
	}
	if j.secondary != nil {
		return j.secondary.Size(ctx, address)
	}
	return 0, false
}

func (j *JoinedStorage) BatchHas(ctx context.Context, addresses []string) ([]string, error) {
	var primaryMissing []string
	if batchPrimary, ok := j.primary.(BatchStorage); ok {
		missing, err := batchPrimary.BatchHas(ctx, addresses)
		if err != nil {
			return nil, err
		}
		primaryMissing = missing
	} else {
		for _, addr := range addresses {
			if !j.primary.Has(ctx, addr) {
				primaryMissing = append(primaryMissing, addr)
			}
		}
	}

	if j.secondary == nil || len(primaryMissing) == 0 {
		return primaryMissing, nil
	}

	var finalMissing []string
	if batchSecondary, ok := j.secondary.(BatchStorage); ok {
		return batchSecondary.BatchHas(ctx, primaryMissing)
	} else {
		for _, addr := range primaryMissing {
			if !j.secondary.Has(ctx, addr) {
				finalMissing = append(finalMissing, addr)
			}
		}
	}
	return finalMissing, nil
}

func (j *JoinedStorage) BatchStore(ctx context.Context, blocks map[string]io.Reader) error {
	if batchPrimary, ok := j.primary.(BatchStorage); ok {
		return batchPrimary.BatchStore(ctx, blocks)
	}
	for addr, r := range blocks {
		success, err := j.primary.StoreAt(ctx, addr, r)
		if err != nil {
			return err
		}
		if !success {
			return context.Canceled
		}
	}
	return nil
}

// WithWriteTag propagates write tag restriction to underlying storage backends.
func (j *JoinedStorage) WithWriteTag(tag string) Storage {
	newPrimary := j.primary
	if ts, ok := j.primary.(TaggedStorage); ok {
		newPrimary = ts.WithWriteTag(tag)
	}
	newSecondary := j.secondary
	if ts, ok := j.secondary.(TaggedStorage); ok {
		newSecondary = ts.WithWriteTag(tag)
	}
	return NewJoinedStorage(newPrimary, newSecondary)
}

var _ Storage = (*JoinedStorage)(nil)
var _ BatchStorage = (*JoinedStorage)(nil)
var _ TaggedStorage = (*JoinedStorage)(nil)
