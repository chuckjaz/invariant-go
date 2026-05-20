package kv

import (
	"context"
	"fmt"
	"io"
	"sync"

	"invariant/internal/content"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// Store represents the Key-Value service orchestration layer.
type Store struct {
	mu         sync.RWMutex
	slotClient slots.Slots
	slotID     string
	slotAuth   []byte
	storage    storage.Storage
	journal    *Journal
	btree      *BTree
	cache      *Cache
	bTreeRoot  string
	seqCounter uint64

	pendingRecords      []Record
	bTreeMergeThreshold int
}

func NewStore(
	ctx context.Context,
	slotClient slots.Slots,
	slotID string,
	slotAuth []byte,
	storage storage.Storage,
	journalDir string,
	maxCacheSize int,
	bTreeMergeThreshold int,
	journalFlushThreshold int,
) (*Store, error) {
	s := &Store{
		slotClient:          slotClient,
		slotID:              slotID,
		slotAuth:            slotAuth,
		storage:             storage,
		cache:               NewCache(maxCacheSize),
		btree:               NewBTree(storage, 100), // MaxKeys = 100
		bTreeMergeThreshold: bTreeMergeThreshold,
	}

	// 1. Get B-Tree root from slot
	rootAddr, err := slotClient.Get(ctx, slotID)
	if err != nil && err != slots.ErrSlotNotFound {
		return nil, err
	}
	s.bTreeRoot = rootAddr

	var lastJournal *content.ContentLink
	if s.bTreeRoot != "" {
		// 2. Load B-Tree root to get LastJournal
		rootNode, err := s.btree.loadNode(ctx, s.bTreeRoot)
		if err != nil {
			return nil, err
		}
		lastJournal = rootNode.LastJournal
	}

	// 3. Initialize Journal
	j, err := NewJournal(journalDir, storage, lastJournal, journalFlushThreshold)
	if err != nil {
		return nil, err
	}
	s.journal = j

	// 4. Load local journals and replay into cache
	localRecs, err := s.journal.LoadLocalJournals()
	if err != nil {
		return nil, err
	}

	for _, rec := range localRecs {
		if rec.Sequence > s.seqCounter {
			s.seqCounter = rec.Sequence
		}
		s.cache.Add(rec, false)
		s.pendingRecords = append(s.pendingRecords, rec)
	}

	return s, nil
}

func (s *Store) Close() error {
	return s.journal.Close()
}

// Put adds a new key-value pair.
func (s *Store) Put(ctx context.Context, key string, value []byte) (uint64, error) {
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
func (s *Store) mergeToBTree(ctx context.Context) error {
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
	if s.bTreeRoot == "" {
		err = s.slotClient.Create(ctx, s.slotID, newRoot, "")
	} else {
		err = s.slotClient.Update(ctx, s.slotID, newRoot, s.bTreeRoot, s.slotAuth)
	}
	if err != nil {
		return err
	}

	// Update local state
	s.bTreeRoot = newRoot
	maxSeq := s.pendingRecords[len(s.pendingRecords)-1].Sequence
	s.pendingRecords = nil

	// Mark cache items as in BTree so they can be evicted
	s.cache.MarkInBTree(maxSeq)

	return nil
}

// Get retrieves the latest value for a key.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	// Try cache first
	if rec, ok := s.cache.Get(key); ok {
		return rec.Value, nil
	}

	// Cache miss, consult B-Tree
	s.mu.RLock()
	rootAddr := s.bTreeRoot
	s.mu.RUnlock()

	valEntry, found, err := s.btree.Search(ctx, rootAddr, key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("key not found: %s", key)
	}

	if valEntry.Link != nil {
		rc, err := content.Read(*valEntry.Link, s.storage, s.slotClient)
		if err != nil {
			return nil, fmt.Errorf("value block not found: %v", err)
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}

	return valEntry.Inline, nil
}
