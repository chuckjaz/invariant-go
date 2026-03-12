package storage

import (
	"context"
	"io"
)

// Storage dictates the necessary requirements for standard invariant byte chunk blocks mapping
type Storage interface {
	Has(ctx context.Context, address string) bool
	Get(ctx context.Context, address string) (io.ReadCloser, bool)
	Store(ctx context.Context, r io.Reader) (string, error)
	StoreAt(ctx context.Context, address string, r io.Reader) (bool, error)
	Size(ctx context.Context, address string) (int64, bool)
}

// SyncStorage is an optional interface allowing storage backends to persist metadata/data.
type SyncStorage interface {
	Storage
	Sync(ctx context.Context) error
}

// ControlledStorage adds capabilities to iterate and subscribe to a Storage
type ControlledStorage interface {
	Storage
	List(ctx context.Context, chunkSize int) <-chan []string
	Subscribe(ctx context.Context) <-chan string
	Remove(ctx context.Context, address string) (bool, error)
}

// StorageFetchRequest represents a request to fetch a block from another service
type StorageFetchRequest struct {
	Address   string `json:"address"`
	Container string `json:"container"`
}
