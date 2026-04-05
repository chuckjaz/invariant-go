package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
)

type hashingStorage struct{}

// NewHashingStorage creates a mock Storage instance that calculates SHA256 content addresses
// without accumulating active file bytes into RAM dynamically saving OOM buffers entirely natively.
func NewHashingStorage() Storage {
	return &hashingStorage{}
}

func (s *hashingStorage) ID() string {
	return "hashing-storage"
}

func (s *hashingStorage) Has(ctx context.Context, address string) bool {
	return false
}

func (s *hashingStorage) Get(ctx context.Context, address string) (io.ReadCloser, bool) {
	return nil, false
}

func (s *hashingStorage) Store(ctx context.Context, r io.Reader) (string, error) {
	hash := sha256.New()
	_, err := io.Copy(hash, r)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *hashingStorage) StoreAt(ctx context.Context, address string, r io.Reader) (bool, error) {
	// Discard content to satisfy streams without caching into memory boundaries natively
	_, err := io.Copy(io.Discard, r)
	return true, err
}

func (s *hashingStorage) Size(ctx context.Context, address string) (int64, bool) {
	return 0, false
}

func (s *hashingStorage) List(ctx context.Context, chunkSize int) <-chan []string {
	ch := make(chan []string)
	close(ch)
	return ch
}

func (s *hashingStorage) Remove(ctx context.Context, address string) (bool, error) {
	return true, nil
}
