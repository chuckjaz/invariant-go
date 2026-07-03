package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sync"
)

type inMemoryStorage struct {
	mu    sync.RWMutex
	store map[string][]byte
}

func newInMemoryStorage() *inMemoryStorage {
	return &inMemoryStorage{
		store: make(map[string][]byte),
	}
}

func (s *inMemoryStorage) Has(ctx context.Context, address string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.store[address]
	return ok
}

func (s *inMemoryStorage) Get(ctx context.Context, address string) (io.ReadCloser, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.store[address]
	if !ok {
		return nil, false
	}
	return io.NopCloser(bytes.NewReader(data)), true
}

func (s *inMemoryStorage) Store(ctx context.Context, r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	address := hex.EncodeToString(hash[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[address] = data
	return address, nil
}

func (s *inMemoryStorage) StoreAt(ctx context.Context, address string, r io.Reader) (bool, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[address] = data
	return true, nil
}

func (s *inMemoryStorage) Size(ctx context.Context, address string) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.store[address]
	if !ok {
		return 0, false
	}
	return int64(len(data)), true
}
