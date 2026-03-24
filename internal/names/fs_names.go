package names

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

	"invariant/internal/identity"
	"invariant/internal/journal"
)

// Assert that FileSystemNames implements the Names interface
var _ Names = (*FileSystemNames)(nil)

// Assert that FileSystemNames implements the identity.Provider interface
var _ identity.Provider = (*FileSystemNames)(nil)

type FileSystemNames struct {
	id    string
	store *journal.Store[string, NameEntry]
}

func NewFileSystemNames(baseDir string, snapshotInterval time.Duration) (*FileSystemNames, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, err
	}

	idPath := filepath.Join(baseDir, "id")
	var id string
	if data, err := os.ReadFile(idPath); err == nil && len(data) == 64 {
		id = string(data)
	} else {
		idBytes := make([]byte, 32)
		rand.Read(idBytes)
		id = hex.EncodeToString(idBytes)
		os.WriteFile(idPath, []byte(id), 0644)
	}

	store, err := journal.NewStore[string, NameEntry](baseDir, snapshotInterval)
	if err != nil {
		return nil, err
	}

	return &FileSystemNames{
		id:    id,
		store: store,
	}, nil
}

func (s *FileSystemNames) ID() string {
	return s.id
}

func (s *FileSystemNames) Close() error {
	return s.store.Close()
}

func (s *FileSystemNames) Get(ctx context.Context, name string) (NameEntry, error) {
	entry, ok := s.store.Get(name)
	if !ok {
		return NameEntry{}, ErrNotFound
	}

	tokensCopy := make([]string, len(entry.Tokens))
	copy(tokensCopy, entry.Tokens)
	return NameEntry{
		Value:  entry.Value,
		Tokens: tokensCopy,
	}, nil
}

func (s *FileSystemNames) Put(ctx context.Context, name string, value string, tokens []string) error {
	tokensCopy := make([]string, len(tokens))
	copy(tokensCopy, tokens)

	return s.store.Put(name, NameEntry{Value: value, Tokens: tokensCopy}, nil)
}

func (s *FileSystemNames) Delete(ctx context.Context, name string, expectedValue string) error {
	return s.store.Delete(name, func(store map[string]NameEntry) error {
		existing, ok := store[name]
		if !ok {
			return ErrNotFound
		}

		if expectedValue != "" && existing.Value != expectedValue {
			// ETag mismatch
			return ErrPreconditionFailed
		}
		return nil
	})
}
