package main

import (
	"bytes"
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"invariant/internal/storage"
)

type BatchingTrackingStorage struct {
	Backend       storage.BatchStorage
	Fallback      storage.Storage
	BytesUploaded *uint64

	// StoreAt buffering
	storeMu     sync.Mutex
	storeBuffer map[string][]byte
	storeSize   int
	flushMu     sync.Mutex

	// Has buffering
	hasMu      sync.Mutex
	hasWaiters map[string][]chan bool
	hasTimer   *time.Timer
}

func NewBatchingTrackingStorage(store storage.Storage, bytesUploaded *uint64) *BatchingTrackingStorage {
	var backend storage.BatchStorage
	if b, ok := store.(storage.BatchStorage); ok {
		backend = b
	}

	b := &BatchingTrackingStorage{
		Backend:       backend,
		Fallback:      store,
		BytesUploaded: bytesUploaded,
		storeBuffer:   make(map[string][]byte),
		hasWaiters:    make(map[string][]chan bool),
	}

	// Start a background flusher for StoreAt just in case it doesn't reach the size limit
	go func() {
		for {
			time.Sleep(20 * time.Millisecond)
			b.FlushStore(context.Background())
		}
	}()

	return b
}

func (b *BatchingTrackingStorage) Size(ctx context.Context, address string) (int64, bool) {
	return b.Fallback.Size(ctx, address)
}

func (b *BatchingTrackingStorage) Get(ctx context.Context, address string) (io.ReadCloser, bool) {
	return b.Fallback.Get(ctx, address)
}

func (b *BatchingTrackingStorage) Store(ctx context.Context, r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	if b.BytesUploaded != nil {
		atomic.AddUint64(b.BytesUploaded, uint64(len(data)))
	}
	return b.Fallback.Store(ctx, bytes.NewReader(data))
}

func (b *BatchingTrackingStorage) Has(ctx context.Context, address string) bool {
	if b.Backend == nil {
		return b.Fallback.Has(ctx, address)
	}

	b.hasMu.Lock()
	ch := make(chan bool, 1)
	b.hasWaiters[address] = append(b.hasWaiters[address], ch)

	shouldFlush := len(b.hasWaiters) >= 100

	if b.hasTimer == nil {
		b.hasTimer = time.AfterFunc(1*time.Millisecond, func() {
			b.FlushHas(context.Background())
		})
	}
	b.hasMu.Unlock()

	if shouldFlush {
		b.FlushHas(ctx)
	}

	return <-ch
}

func (b *BatchingTrackingStorage) FlushHas(ctx context.Context) {
	b.hasMu.Lock()
	if len(b.hasWaiters) == 0 {
		b.hasMu.Unlock()
		return
	}

	waiters := b.hasWaiters
	b.hasWaiters = make(map[string][]chan bool)
	if b.hasTimer != nil {
		b.hasTimer.Stop()
		b.hasTimer = nil
	}
	b.hasMu.Unlock()

	var addresses []string
	for addr := range waiters {
		addresses = append(addresses, addr)
	}

	missingAddrs, err := b.Backend.BatchHas(ctx, addresses)

	missingSet := make(map[string]bool)
	if err == nil {
		for _, addr := range missingAddrs {
			missingSet[addr] = true
		}
	} else {
		// Fallback to true (not missing) or something else?
		// If BatchHas fails, we fallback to Has individually
		for _, addr := range addresses {
			if !b.Fallback.Has(ctx, addr) {
				missingSet[addr] = true
			}
		}
	}

	// Notify all waiters
	for addr, chans := range waiters {
		hasResult := !missingSet[addr]
		for _, ch := range chans {
			ch <- hasResult
		}
	}
}

func (b *BatchingTrackingStorage) StoreAt(ctx context.Context, address string, r io.Reader) (bool, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return false, err
	}

	if b.BytesUploaded != nil {
		atomic.AddUint64(b.BytesUploaded, uint64(len(data)))
	}

	if b.Backend == nil {
		return b.Fallback.StoreAt(ctx, address, bytes.NewReader(data))
	}

	b.storeMu.Lock()
	b.storeBuffer[address] = data
	b.storeSize += len(data)

	shouldFlush := b.storeSize >= 5*1024*1024 || len(b.storeBuffer) >= 100
	b.storeMu.Unlock()

	if shouldFlush {
		err := b.FlushStore(ctx)
		return true, err
	}

	return true, nil
}

func (b *BatchingTrackingStorage) FlushStore(ctx context.Context) error {
	b.flushMu.Lock()
	defer b.flushMu.Unlock()

	b.storeMu.Lock()
	if len(b.storeBuffer) == 0 {
		b.storeMu.Unlock()
		return nil
	}

	batch := b.storeBuffer
	b.storeBuffer = make(map[string][]byte)
	b.storeSize = 0
	b.storeMu.Unlock()

	blocks := make(map[string]io.Reader)
	for addr, data := range batch {
		blocks[addr] = bytes.NewReader(data)
	}

	if b.Backend != nil {
		return b.Backend.BatchStore(ctx, blocks)
	}

	for addr, r := range blocks {
		b.Fallback.StoreAt(ctx, addr, r)
	}
	return nil
}
