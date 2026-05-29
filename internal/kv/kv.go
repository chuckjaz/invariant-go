package kv

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"invariant/internal/content"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// Store represents the Key-Value service orchestration layer.
type KeyValueStore interface {
	Put(ctx context.Context, key string, value []byte) (uint64, error)
	Get(ctx context.Context, key string) ([]byte, error)
	BatchPut(ctx context.Context, kvs map[string][]byte) (uint64, error)
	BatchGet(ctx context.Context, keys []string) (map[string][]byte, error)
}

// FileKeyValueStore represents the Key-Value service orchestration layer.
type FileKeyValueStore struct {
	mu              sync.RWMutex
	slotClient      slots.Slots
	btreeSlotID     string
	btreeSlotAuth   []byte
	journalSlotID   string
	journalSlotAuth []byte
	storage         storage.Storage
	journal         *Journal
	btree           *BTree
	cache           *Cache
	bTreeRoot       *content.ContentLink
	seqCounter      uint64

	pendingRecords      []Record
	bTreeMergeThreshold int
}

func NewFileKeyValueStore(
	ctx context.Context,
	slotClient slots.Slots,
	btreeSlotID string,
	btreeSlotAuth []byte,
	journalSlotID string,
	journalSlotAuth []byte,
	storage storage.Storage,
	journalDir string,
	maxCacheSize int,
	bTreeMergeThreshold int,
	journalFlushThreshold int,
	opts content.WriterOptions,
) (*FileKeyValueStore, error) {
	s := &FileKeyValueStore{
		slotClient:          slotClient,
		btreeSlotID:         btreeSlotID,
		btreeSlotAuth:       btreeSlotAuth,
		journalSlotID:       journalSlotID,
		journalSlotAuth:     journalSlotAuth,
		storage:             storage,
		cache:               NewCache(maxCacheSize),
		btree:               NewBTree(storage, slotClient, 100, opts), // MaxKeys = 100
		bTreeMergeThreshold: bTreeMergeThreshold,
	}

	// 1. Get B-Tree root from slot
	rootAddrStr, err := slotClient.Get(ctx, btreeSlotID)
	if err != nil && err != slots.ErrSlotNotFound {
		return nil, err
	}

	if rootAddrStr != "" {
		s.bTreeRoot = &content.ContentLink{}
		if err := json.Unmarshal([]byte(rootAddrStr), s.bTreeRoot); err != nil {
			return nil, fmt.Errorf("failed to parse slot data as ContentLink: %v", err)
		}
	}

	var lastJournal *content.ContentLink
	journalAddrStr, err := slotClient.Get(ctx, journalSlotID)
	if err != nil && err != slots.ErrSlotNotFound {
		return nil, err
	}

	if journalAddrStr != "" {
		lastJournal = &content.ContentLink{}
		if err := json.Unmarshal([]byte(journalAddrStr), lastJournal); err != nil {
			return nil, fmt.Errorf("failed to parse journal slot data as ContentLink: %v", err)
		}
	}

	// 3. Initialize Journal
	j, err := NewJournal(journalDir, storage, slotClient, journalSlotID, journalSlotAuth, lastJournal, journalFlushThreshold, opts)
	if err != nil {
		return nil, err
	}
	s.journal = j

	var btreeLastJournal *content.ContentLink
	if s.bTreeRoot != nil {
		rootNode, err := s.btree.loadNode(ctx, *s.bTreeRoot)
		if err == nil && rootNode != nil {
			btreeLastJournal = rootNode.LastJournal
		}
	}

	// 4. Load remote journals and replay into cache
	remoteRecs, err := s.journal.LoadRemoteJournals(ctx, btreeLastJournal)
	if err != nil {
		return nil, err
	}

	// 5. Load local journals and replay into cache
	localRecs, err := s.journal.LoadLocalJournals()
	if err != nil {
		return nil, err
	}

	allRecs := append(remoteRecs, localRecs...)

	for _, rec := range allRecs {
		if rec.Sequence > s.seqCounter {
			s.seqCounter = rec.Sequence
		}
		s.cache.Add(rec, false)
		s.pendingRecords = append(s.pendingRecords, rec)
	}

	return s, nil
}

func (s *FileKeyValueStore) Close() error {
	return s.journal.Close()
}

// Put adds a new key-value pair.
func (s *FileKeyValueStore) Put(ctx context.Context, key string, value []byte) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seqCounter++
	seq := s.seqCounter
	rec := Record{
		Key:      key,
		Sequence: seq,
		Value:    value,
	}

	// 1. Write to local journal
	flushed, err := s.journal.Append(ctx, rec)
	if err != nil {
		return 0, err
	}

	// 2. Add to cache (not in BTree yet)
	s.cache.Add(rec, false)
	s.pendingRecords = append(s.pendingRecords, rec)

	// 3. Merge to BTree if threshold reached
	if len(s.pendingRecords) >= s.bTreeMergeThreshold || flushed {
		err := s.mergeToBTree(ctx)
		if err != nil {
			// Log error, but we still successfully journaled
			fmt.Printf("Error merging to BTree: %v\n", err)
		}
	}

	return seq, nil
}

// mergeToBTree takes pending records and inserts them into the B-Tree, updating the slot.
// MUST be called with s.mu Lock held.
func (s *FileKeyValueStore) mergeToBTree(ctx context.Context) error {
	if len(s.pendingRecords) == 0 {
		return nil
	}

	// Upload journal if not flushed yet to ensure we have a valid lastJournal
	if s.journal.entries > 0 {
		if err := s.journal.Flush(ctx); err != nil {
			return err
		}
	}

	lastJournal := s.journal.PreviousJournal()

	// Insert into B-Tree
	newRoot, err := s.btree.InsertBatch(ctx, s.bTreeRoot, s.pendingRecords, lastJournal)
	if err != nil {
		return err
	}

	// Update slot
	newRootBytes, err := json.Marshal(newRoot)
	if err != nil {
		return err
	}
	newRootStr := string(newRootBytes)

	if s.bTreeRoot == nil {
		err = s.slotClient.Create(ctx, s.btreeSlotID, newRootStr, "")
	} else {
		oldRootBytes, _ := json.Marshal(s.bTreeRoot)
		err = s.slotClient.Update(ctx, s.btreeSlotID, newRootStr, string(oldRootBytes), s.btreeSlotAuth)
	}
	if err != nil {
		return err
	}

	// Update local state
	s.bTreeRoot = &newRoot
	maxSeq := s.pendingRecords[len(s.pendingRecords)-1].Sequence
	s.pendingRecords = nil

	// Mark cache items as in BTree so they can be evicted
	s.cache.MarkInBTree(maxSeq)

	return nil
}

// Get retrieves the latest value for a key.
func (s *FileKeyValueStore) Get(ctx context.Context, key string) ([]byte, error) {
	// Try cache first
	if rec, ok := s.cache.Get(key); ok {
		return rec.Value, nil
	}

	// Cache miss, consult B-Tree
	s.mu.RLock()
	rootAddr := s.bTreeRoot
	s.mu.RUnlock()

	valEntry, seq, found, err := s.btree.Search(ctx, rootAddr, key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("key not found: %s", key)
	}

	var valBytes []byte
	if valEntry.Link != nil {
		rc, err := content.Read(*valEntry.Link, s.storage, s.slotClient)
		if err != nil {
			return nil, fmt.Errorf("value block not found: %v", err)
		}
		defer rc.Close()
		valBytes, err = io.ReadAll(rc)
		if err != nil {
			return nil, err
		}
	} else {
		valBytes = valEntry.Inline
	}

	// Add the missed item to the cache. Since it was found in the BTree,
	// mark inBTree as true.
	s.cache.Add(Record{
		Key:      key,
		Sequence: seq,
		Value:    valBytes,
	}, true)

	return valBytes, nil
}

// BatchPut adds multiple key-value pairs at once.
func (s *FileKeyValueStore) BatchPut(ctx context.Context, kvs map[string][]byte) (uint64, error) {
	var maxSeq uint64
	for key, val := range kvs {
		seq, err := s.Put(ctx, key, val)
		if err != nil {
			return 0, err
		}
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	return maxSeq, nil
}

// BatchGet fetches the values for multiple keys at once.
func (s *FileKeyValueStore) BatchGet(ctx context.Context, keys []string) (map[string][]byte, error) {
	results := make(map[string][]byte)
	for _, key := range keys {
		val, err := s.Get(ctx, key)
		if err == nil && val != nil {
			results[key] = val
		}
	}
	return results, nil
}
