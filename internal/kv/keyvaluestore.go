package kv

import "context"

// KeyValueStore represents the Key-Value service orchestration layer.
type KeyValueStore interface {
	Put(ctx context.Context, key string, value []byte) (uint64, error)
	Get(ctx context.Context, key string) ([]byte, error)
	BatchPut(ctx context.Context, kvs map[string][]byte) (uint64, error)
	BatchGet(ctx context.Context, keys []string) (map[string][]byte, error)
}
