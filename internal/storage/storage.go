package storage

import "io"

// Storage dictates the necessary requirements for standard invariant byte chunk blocks mapping
type Storage interface {
	Has(address string) bool
	Get(address string) (io.ReadCloser, bool)
	Store(r io.Reader) (string, error)
	StoreAt(address string, r io.Reader) (bool, error)
	Size(address string) (int64, bool)
}

// StorageFetchRequest represents a request to fetch a block from another service
type StorageFetchRequest struct {
	Address   string `json:"address"`
	Container string `json:"container"`
}
