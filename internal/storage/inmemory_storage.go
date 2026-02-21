package storage

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"invariant/internal/identity"
	"io"
	"sync"
)

// Assert that InMemoryStorage implements the Storage interface
var _ Storage = (*InMemoryStorage)(nil)

// Assert that InMemoryStorage implements the identity.Provider interface
var _ identity.Provider = (*InMemoryStorage)(nil)

type InMemoryStorage struct {
	id    string
	mu    sync.RWMutex
	store map[string][]byte
}

func NewInMemoryStorage() *InMemoryStorage {
	idBytes := make([]byte, 32)
	rand.Read(idBytes)
	id := hex.EncodeToString(idBytes)

	return &InMemoryStorage{
		id:    id,
		store: make(map[string][]byte),
	}
}

func (s *InMemoryStorage) ID() string {
	return s.id
}

func (s *InMemoryStorage) Has(address string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.store[address]
	return ok
}

func (s *InMemoryStorage) Get(address string) (io.ReadCloser, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.store[address]
	if !ok {
		return nil, false
	}
	return io.NopCloser(bytes.NewReader(data)), true
}

func (s *InMemoryStorage) Store(r io.Reader) (string, error) {
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

func (s *InMemoryStorage) StoreAt(address string, r io.Reader) (bool, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return false, err
	}
	hash := sha256.Sum256(data)
	expectedAddress := hex.EncodeToString(hash[:])

	if address != expectedAddress {
		return false, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[address] = data
	return true, nil
}

func (s *InMemoryStorage) Size(address string) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.store[address]
	if !ok {
		return 0, false
	}
	return int64(len(data)), true
}
