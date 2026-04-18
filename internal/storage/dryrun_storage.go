package storage

import (
	"context"
	"io"
	"sync"
)

type dryRunStorage struct {
	hasher Storage
	seen   sync.Map
}

// NewDryRunStorage creates a mock Storage instance that short-circuits data persistence
// by unconditionally asserting the file tree content map already resolves locally.
func NewDryRunStorage() Storage {
	return &dryRunStorage{hasher: NewHashingStorage()}
}

func (s *dryRunStorage) ID() string {
	return "dryrun-storage"
}

func (s *dryRunStorage) Has(ctx context.Context, address string) bool {
	_, ok := s.seen.Load(address)
	return ok
}

func (s *dryRunStorage) Get(ctx context.Context, address string) (io.ReadCloser, bool) {
	return nil, false
}

func (s *dryRunStorage) Store(ctx context.Context, r io.Reader) (string, error) {
	addr, err := s.hasher.Store(ctx, r)
	if err == nil {
		s.seen.Store(addr, true)
	}
	return addr, err
}

func (s *dryRunStorage) StoreAt(ctx context.Context, address string, r io.Reader) (bool, error) {
	s.seen.Store(address, true)
	return true, nil
}

func (s *dryRunStorage) Size(ctx context.Context, address string) (int64, bool) {
	return 0, false
}

func (s *dryRunStorage) List(ctx context.Context, chunkSize int) <-chan []string {
	ch := make(chan []string)
	close(ch)
	return ch
}

func (s *dryRunStorage) Remove(ctx context.Context, address string) (bool, error) {
	return true, nil
}
