package names

import (
	"crypto/rand"
	"encoding/hex"
	"invariant/internal/identity"
	"sync"
)

// Assert that InMemoryNames implements the Names interface
var _ Names = (*InMemoryNames)(nil)

// Assert that InMemoryNames implements the identity.Provider interface
var _ identity.Provider = (*InMemoryNames)(nil)

type InMemoryNames struct {
	id    string
	mu    sync.RWMutex
	store map[string]NameEntry
}

func NewInMemoryNames() *InMemoryNames {
	idBytes := make([]byte, 32)
	rand.Read(idBytes)
	id := hex.EncodeToString(idBytes)

	return &InMemoryNames{
		id:    id,
		store: make(map[string]NameEntry),
	}
}

func (s *InMemoryNames) ID() string {
	return s.id
}

func (s *InMemoryNames) Get(name string) (NameEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.store[name]
	if !ok {
		return NameEntry{}, ErrNotFound
	}
	// Return a copy of tokens to prevent modification
	tokensCopy := make([]string, len(entry.Tokens))
	copy(tokensCopy, entry.Tokens)
	return NameEntry{
		Value:  entry.Value,
		Tokens: tokensCopy,
	}, nil
}

func (s *InMemoryNames) Put(name string, value string, tokens []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tokensCopy := make([]string, len(tokens))
	copy(tokensCopy, tokens)

	s.store[name] = NameEntry{
		Value:  value,
		Tokens: tokensCopy,
	}
	return nil
}

func (s *InMemoryNames) Delete(name string, expectedValue string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.store[name]
	if !ok {
		return ErrNotFound
	}

	if expectedValue != "" && entry.Value != expectedValue {
		// ETag mismatch
		return ErrPreconditionFailed
	}

	delete(s.store, name)
	return nil
}
