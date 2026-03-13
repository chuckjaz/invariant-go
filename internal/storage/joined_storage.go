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
