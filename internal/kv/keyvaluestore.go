package kv

import "context"

// ValueWithSequence wraps the retrieved value along with its sequence number.
type ValueWithSequence struct {
	Value    []byte
	Sequence uint64
}

// HistoryPage wraps a page of historical values and a flag indicating if more exist.
type HistoryPage struct {
	Values  []ValueWithSequence
	HasMore bool
}

// KeyValueStore represents the Key-Value service orchestration layer.
type KeyValueStore interface {
	Put(ctx context.Context, key string, value []byte) (uint64, error)
	Get(ctx context.Context, key string) ([]byte, uint64, error)
	GetHistory(ctx context.Context, key string, minSeq uint64, maxSeq uint64, pageSize int) (HistoryPage, error)
}

// BatchKeyValueStore provides batched key-value operations.
type BatchKeyValueStore interface {
	BatchPut(ctx context.Context, kvs map[string][]byte) (uint64, error)
	BatchGet(ctx context.Context, keys []string) (map[string]ValueWithSequence, error)
	BatchGetHistory(ctx context.Context, keys []string, minSeq uint64, maxSeq uint64, pageSize int) (map[string]HistoryPage, error)
}
