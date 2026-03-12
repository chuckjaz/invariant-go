package storage

import (
	"context"
	"io"
)

// Storage dictates the necessary requirements for standard invariant byte chunk blocks mapping
type Storage interface {
	Has(address string) bool
	Get(address string) (io.ReadCloser, bool)
	Store(r io.Reader) (string, error)
	StoreAt(address string, r io.Reader) (bool, error)
	Size(address string) (int64, bool)
}

// SyncStorage is an optional interface allowing storage backends to persist metadata/data.
type SyncStorage interface {
	Storage
	Sync(ctx context.Context) error
}

// ControlledStorage adds capabilities to iterate and subscribe to a Storage
type ControlledStorage interface {
	Storage
	List(chunkSize int) <-chan []string
	Subscribe() <-chan string
	Remove(address string) (bool, error)
}

// StorageFetchRequest represents a request to fetch a block from another service
type StorageFetchRequest struct {
	Address   string `json:"address"`
	Container string `json:"container"`
}
