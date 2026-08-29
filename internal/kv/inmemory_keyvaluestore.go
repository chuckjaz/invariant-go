package kv

import (
	"context"
	"fmt"
	"sync"
)

var _ KeyValueStore = (*MemoryKeyValueStore)(nil)
var _ BatchKeyValueStore = (*MemoryKeyValueStore)(nil)

// MemoryKeyValueStore is an in-memory implementation of KeyValueStore for testing and standalone use.
type MemoryKeyValueStore struct {
	mu   sync.RWMutex
	data map[string][]byte
	seq  uint64
}

// NewMemoryKeyValueStore creates a new in-memory KeyValueStore.
func NewMemoryKeyValueStore() *MemoryKeyValueStore {
	return &MemoryKeyValueStore{
		data: make(map[string][]byte),
	}
}

func (m *MemoryKeyValueStore) Get(ctx context.Context, txID *uint64, key string) ([]byte, uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.data[key]
	if !ok {
		return nil, 0, fmt.Errorf("key not found: %s", key)
	}
	cp := make([]byte, len(val))
	copy(cp, val)
	return cp, m.seq, nil
}

func (m *MemoryKeyValueStore) Put(ctx context.Context, txID *uint64, key string, value []byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[key] = cp
	return m.seq, nil
}

func (m *MemoryKeyValueStore) BatchGet(ctx context.Context, txID *uint64, keys []string) (map[string]ValueWithTransaction, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make(map[string]ValueWithTransaction, len(keys))
	for _, k := range keys {
		if val, ok := m.data[k]; ok {
			cp := make([]byte, len(val))
			copy(cp, val)
			res[k] = ValueWithTransaction{Value: cp, TransactionID: m.seq}
		}
	}
	return res, nil
}

func (m *MemoryKeyValueStore) BatchPut(ctx context.Context, txID *uint64, kvs map[string][]byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	for k, val := range kvs {
		cp := make([]byte, len(val))
		copy(cp, val)
		m.data[k] = cp
	}
	return m.seq, nil
}

func (m *MemoryKeyValueStore) BatchGetHistory(ctx context.Context, txID *uint64, keys []string, minTxID uint64, maxTxID uint64, pageSize int) (map[string]HistoryPage, error) {
	res := make(map[string]HistoryPage, len(keys))
	for _, k := range keys {
		h, err := m.GetHistory(ctx, txID, k, minTxID, maxTxID, pageSize)
		if err == nil {
			res[k] = h
		}
	}
	return res, nil
}

func (m *MemoryKeyValueStore) GetHistory(ctx context.Context, txID *uint64, key string, minTxID uint64, maxTxID uint64, pageSize int) (HistoryPage, error) {
	val, seq, err := m.Get(ctx, txID, key)
	if err != nil {
		return HistoryPage{}, err
	}
	return HistoryPage{
		Values: []ValueWithTransaction{{Value: val, TransactionID: seq}},
	}, nil
}

func (m *MemoryKeyValueStore) StartTransaction(ctx context.Context, sequential bool) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	return m.seq, nil
}

func (m *MemoryKeyValueStore) CommitTransaction(ctx context.Context, txID uint64) error {
	return nil
}

func (m *MemoryKeyValueStore) AbortTransaction(ctx context.Context, txID uint64) error {
	return nil
}

func (m *MemoryKeyValueStore) CreateCheckpoint(ctx context.Context) (uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.seq, nil
}
