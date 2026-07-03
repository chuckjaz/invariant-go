package kv

import "context"

// ValueWithTransaction wraps the retrieved value along with its transaction ID.
type ValueWithTransaction struct {
	Value         []byte
	TransactionID uint64
}

// HistoryPage wraps a page of historical values and a flag indicating if more exist.
type HistoryPage struct {
	Values  []ValueWithTransaction
	HasMore bool
}

// KeyValueStoreReader specifies read operations for Key-Value store.
type KeyValueStoreReader interface {
	Get(ctx context.Context, txID *uint64, key string) ([]byte, uint64, error)
	GetHistory(ctx context.Context, txID *uint64, key string, minTxID uint64, maxTxID uint64, pageSize int) (HistoryPage, error)
}

// BatchKeyValueStoreReader specifies batch read operations for Key-Value store.
type BatchKeyValueStoreReader interface {
	BatchGet(ctx context.Context, txID *uint64, keys []string) (map[string]ValueWithTransaction, error)
	BatchGetHistory(ctx context.Context, txID *uint64, keys []string, minTxID uint64, maxTxID uint64, pageSize int) (map[string]HistoryPage, error)
}

// KeyValueStore represents the Key-Value service orchestration layer.
type KeyValueStore interface {
	KeyValueStoreReader
	StartTransaction(ctx context.Context, sequential bool) (uint64, error)
	CommitTransaction(ctx context.Context, txID uint64) error
	AbortTransaction(ctx context.Context, txID uint64) error
	CreateCheckpoint(ctx context.Context) (uint64, error)
	Put(ctx context.Context, txID *uint64, key string, value []byte) (uint64, error)
}

// BatchKeyValueStore provides batched key-value operations.
type BatchKeyValueStore interface {
	BatchKeyValueStoreReader
	KeyValueStore
	BatchPut(ctx context.Context, txID *uint64, kvs map[string][]byte) (uint64, error)
}
