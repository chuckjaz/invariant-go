package kv

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"invariant/internal/content"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

type TxState int

const (
	TxActive TxState = iota
	TxCommitted
	TxAborted
)

type Transaction struct {
	ID         uint64
	State      TxState
	Sequential bool
	ReadSet    map[string]struct{}
	WriteSet   map[string]struct{}
	ActiveTxs  map[uint64]struct{}
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
	txCounter       uint64

	transactions map[uint64]*Transaction
	activeTxs    map[uint64]struct{}

	pendingRecords      []Record
	pendingIndex        map[string][]int
	bTreeMergeThreshold int

	mergingRecords []Record
	mergingIndex   map[string][]int
	mergeMu        sync.Mutex
	isMerging      bool
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
		transactions:        make(map[uint64]*Transaction),
		activeTxs:           make(map[uint64]struct{}),
		pendingIndex:        make(map[string][]int),
		mergingIndex:        make(map[string][]int),
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
			if rootNode.MaxTxID > s.txCounter {
				s.txCounter = rootNode.MaxTxID
			}
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
		if rec.TransactionID > s.txCounter {
			s.txCounter = rec.TransactionID
		}
		s.replayJournalRecord(rec)
	}

	return s, nil
}

func (s *FileKeyValueStore) replayJournalRecord(rec Record) {
	switch rec.Type {
	case RecordTypeTxStart:
		activeCopy := make(map[uint64]struct{})
		for id := range s.activeTxs {
			activeCopy[id] = struct{}{}
		}
		s.transactions[rec.TransactionID] = &Transaction{
			ID:         rec.TransactionID,
			State:      TxActive,
			Sequential: rec.Sequential,
			ReadSet:    make(map[string]struct{}),
			WriteSet:   make(map[string]struct{}),
			ActiveTxs:  activeCopy,
		}
		s.activeTxs[rec.TransactionID] = struct{}{}
	case RecordTypeTxCommit:
		if tx, ok := s.transactions[rec.TransactionID]; ok {
			tx.State = TxCommitted
		}
		delete(s.activeTxs, rec.TransactionID)
	case RecordTypeTxAbort:
		if tx, ok := s.transactions[rec.TransactionID]; ok {
			tx.State = TxAborted
		}
		delete(s.activeTxs, rec.TransactionID)
	case RecordTypeTxCheckpoint:
		s.transactions[rec.TransactionID] = &Transaction{
			ID:         rec.TransactionID,
			State:      TxCommitted,
			Sequential: false,
		}
	case RecordTypePut:
		s.cache.Add(rec, false)
		s.pendingRecords = append(s.pendingRecords, rec)
		s.pendingIndex[rec.Key] = append(s.pendingIndex[rec.Key], len(s.pendingRecords)-1)
		if tx, ok := s.transactions[rec.TransactionID]; ok {
			tx.WriteSet[rec.Key] = struct{}{}
		}
	}
}

func (s *FileKeyValueStore) isVisible(txID uint64, recordTxID uint64) bool {
	if txID == recordTxID {
		return true
	}
	if recordTxID > txID {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isVisibleLocked(txID, recordTxID)
}

func (s *FileKeyValueStore) isVisibleLocked(txID uint64, recordTxID uint64) bool {
	if txID == recordTxID {
		return true
	}
	if recordTxID > txID {
		return false
	}
	reqTx := s.transactions[txID]
	recordTx := s.transactions[recordTxID]
	if recordTx != nil && recordTx.State != TxCommitted {
		return false
	}
	if reqTx != nil {
		if _, ok := reqTx.ActiveTxs[recordTxID]; ok {
			return false
		}
	}
	return true
}

func (s *FileKeyValueStore) getLatestCommittedTxIDLocked(ctx context.Context, key string) (uint64, bool) {
	if indices, ok := s.pendingIndex[key]; ok {
		for i := len(indices) - 1; i >= 0; i-- {
			rec := s.pendingRecords[indices[i]]
			if tx, ok := s.transactions[rec.TransactionID]; ok && tx.State == TxCommitted {
				return rec.TransactionID, true
			}
		}
	}
	if s.isMerging {
		if indices, ok := s.mergingIndex[key]; ok {
			for i := len(indices) - 1; i >= 0; i-- {
				rec := s.mergingRecords[indices[i]]
				if tx, ok := s.transactions[rec.TransactionID]; ok && tx.State == TxCommitted {
					return rec.TransactionID, true
				}
			}
		}
	}

	// Warning: Calling B-Tree search while holding a lock might be slow if a network fetch occurs.
	_, txID, found, _ := s.btree.Search(ctx, s.bTreeRoot, key)
	if found {
		return txID, true
	}
	return 0, false
}

func (s *FileKeyValueStore) Close() error {
	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	return s.journal.Close()
}

func (s *FileKeyValueStore) StartTransaction(ctx context.Context, sequential bool) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.txCounter++
	txID := s.txCounter

	activeCopy := make(map[uint64]struct{})
	for id := range s.activeTxs {
		activeCopy[id] = struct{}{}
	}

	tx := &Transaction{
		ID:         txID,
		State:      TxActive,
		Sequential: sequential,
		ReadSet:    make(map[string]struct{}),
		WriteSet:   make(map[string]struct{}),
		ActiveTxs:  activeCopy,
	}

	s.transactions[txID] = tx
	s.activeTxs[txID] = struct{}{}

	rec := Record{
		Type:          RecordTypeTxStart,
		TransactionID: txID,
		Sequential:    sequential,
	}
	_, err := s.journal.Append(ctx, rec)
	if err != nil {
		delete(s.transactions, txID)
		delete(s.activeTxs, txID)
		return 0, err
	}

	return txID, nil
}

func (s *FileKeyValueStore) CreateCheckpoint(ctx context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.journal.LastRecordType() == RecordTypeTxCheckpoint {
		return s.txCounter, nil
	}

	s.txCounter++
	txID := s.txCounter

	tx := &Transaction{
		ID:         txID,
		State:      TxCommitted,
		Sequential: false,
	}
	s.transactions[txID] = tx

	rec := Record{
		Type:          RecordTypeTxCheckpoint,
		TransactionID: txID,
	}
	_, err := s.journal.Append(ctx, rec)
	return txID, err
}

func (s *FileKeyValueStore) AbortTransaction(ctx context.Context, txID uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, ok := s.transactions[txID]
	if !ok || tx.State != TxActive {
		return fmt.Errorf("invalid or inactive transaction: %d", txID)
	}

	rec := Record{
		Type:          RecordTypeTxAbort,
		TransactionID: txID,
	}
	_, err := s.journal.Append(ctx, rec)
	if err != nil {
		return err
	}

	tx.State = TxAborted
	delete(s.activeTxs, txID)

	for key := range tx.WriteSet {
		s.cache.Invalidate(key, txID)
	}

	return nil
}

func (s *FileKeyValueStore) CommitTransaction(ctx context.Context, txID uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, ok := s.transactions[txID]
	if !ok || tx.State != TxActive {
		return fmt.Errorf("invalid or inactive transaction: %d", txID)
	}

	var conflicts []string
	checkKey := func(key string) {
		latestTxID, found := s.getLatestCommittedTxIDLocked(ctx, key)
		if !found {
			return
		}
		if latestTxID > txID {
			conflicts = append(conflicts, key)
			return
		}
		if _, activeThen := tx.ActiveTxs[latestTxID]; activeThen {
			conflicts = append(conflicts, key)
		}
	}

	for key := range tx.WriteSet {
		checkKey(key)
	}

	if tx.Sequential {
		for key := range tx.ReadSet {
			if _, inWrite := tx.WriteSet[key]; !inWrite {
				checkKey(key)
			}
		}
	}

	if len(conflicts) > 0 {
		rec := Record{
			Type:          RecordTypeTxAbort,
			TransactionID: txID,
		}
		s.journal.Append(ctx, rec)
		tx.State = TxAborted
		delete(s.activeTxs, txID)
		for key := range tx.WriteSet {
			s.cache.Invalidate(key, txID)
		}
		return fmt.Errorf("transaction %d aborted due to conflicts on keys: %v", txID, conflicts)
	}

	rec := Record{
		Type:          RecordTypeTxCommit,
		TransactionID: txID,
	}
	flushed, err := s.journal.Append(ctx, rec)
	if err != nil {
		return err
	}

	tx.State = TxCommitted
	delete(s.activeTxs, txID)

	if len(s.pendingRecords) >= s.bTreeMergeThreshold || flushed {
		s.triggerAsyncMerge(ctx)
	}

	return nil
}

// Put adds a new key-value pair.
func (s *FileKeyValueStore) Put(ctx context.Context, txID *uint64, key string, value []byte) (uint64, error) {
	var id uint64
	var err error
	implicit := false
	if txID == nil {
		id, err = s.StartTransaction(ctx, false)
		if err != nil {
			return 0, err
		}
		implicit = true
		txID = &id
	}

	s.mu.Lock()
	tx, ok := s.transactions[*txID]
	if !ok || tx.State != TxActive {
		s.mu.Unlock()
		return 0, fmt.Errorf("invalid or inactive transaction: %d", *txID)
	}

	rec := Record{
		Type:          RecordTypePut,
		Key:           key,
		TransactionID: *txID,
		Value:         value,
	}

	// 1. Write to local journal
	flushed, err := s.journal.Append(ctx, rec)
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}

	tx.WriteSet[key] = struct{}{}

	// 2. Add to cache and pending
	s.cache.Add(rec, false)
	s.pendingRecords = append(s.pendingRecords, rec)
	s.pendingIndex[key] = append(s.pendingIndex[key], len(s.pendingRecords)-1)

	// 3. Merge to BTree if threshold reached
	if len(s.pendingRecords) >= s.bTreeMergeThreshold || flushed {
		s.triggerAsyncMerge(ctx)
	}
	s.mu.Unlock()

	if implicit {
		err = s.CommitTransaction(ctx, id)
		if err != nil {
			return 0, err
		}
	}

	return *txID, nil
}

// triggerAsyncMerge starts a background B-Tree merge if one isn't already running.
// MUST be called with s.mu Lock held.
func (s *FileKeyValueStore) triggerAsyncMerge(ctx context.Context) {
	if s.isMerging || len(s.pendingRecords) == 0 {
		return
	}

	var toMerge []Record
	var newPending []Record

	for _, rec := range s.pendingRecords {
		tx := s.transactions[rec.TransactionID]
		if tx != nil && (tx.State == TxCommitted || tx.State == TxAborted || rec.Type == RecordTypeTxCheckpoint) {
			if tx.State == TxCommitted || rec.Type == RecordTypeTxCheckpoint {
				toMerge = append(toMerge, rec)
			}
		} else {
			newPending = append(newPending, rec)
		}
	}

	if len(toMerge) == 0 {
		s.pendingRecords = newPending
		s.pendingIndex = make(map[string][]int)
		for i, rec := range newPending {
			s.pendingIndex[rec.Key] = append(s.pendingIndex[rec.Key], i)
		}
		return
	}

	s.mergingRecords = toMerge
	s.mergingIndex = make(map[string][]int)
	for i, rec := range toMerge {
		s.mergingIndex[rec.Key] = append(s.mergingIndex[rec.Key], i)
	}

	s.pendingRecords = newPending
	s.pendingIndex = make(map[string][]int)
	for i, rec := range newPending {
		s.pendingIndex[rec.Key] = append(s.pendingIndex[rec.Key], i)
	}

	lastJournal := s.journal.PreviousJournal()
	s.isMerging = true

	go func(records []Record, jLink *content.ContentLink) {
		s.mergeMu.Lock()
		defer s.mergeMu.Unlock()

		err := s.performMergeToBTree(context.Background(), records, jLink)
		if err != nil {
			fmt.Printf("Error merging to BTree: %v\n", err)
		}

		s.mu.Lock()
		s.mergingRecords = nil
		s.mergingIndex = nil
		s.isMerging = false
		s.mu.Unlock()
	}(s.mergingRecords, lastJournal)
}

func (s *FileKeyValueStore) performMergeToBTree(ctx context.Context, records []Record, lastJournal *content.ContentLink) error {

	var maxTxID uint64
	for _, rec := range records {
		if rec.TransactionID > maxTxID {
			maxTxID = rec.TransactionID
		}
	}

	s.mu.RLock()
	rootAddr := s.bTreeRoot
	txCounter := s.txCounter
	s.mu.RUnlock()

	// Insert into B-Tree
	newRoot, err := s.btree.InsertBatch(ctx, rootAddr, records, lastJournal, txCounter)
	if err != nil {
		return err
	}

	newRootBytes, err := json.Marshal(newRoot)
	if err != nil {
		return err
	}
	newRootStr := string(newRootBytes)

	if rootAddr == nil {
		err = s.slotClient.Create(ctx, s.btreeSlotID, newRootStr, "")
	} else {
		oldRootBytes, _ := json.Marshal(rootAddr)
		err = s.slotClient.Update(ctx, s.btreeSlotID, newRootStr, string(oldRootBytes), s.btreeSlotAuth)
	}
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.bTreeRoot = &newRoot
	s.cache.MarkInBTree(maxTxID)
	s.mu.Unlock()

	return nil
}

// Get retrieves the visible value for a key.
func (s *FileKeyValueStore) Get(ctx context.Context, txID *uint64, key string) ([]byte, uint64, error) {
	var id uint64
	if txID == nil {
		chk, err := s.CreateCheckpoint(ctx)
		if err != nil {
			return nil, 0, err
		}
		id = chk
	} else {
		id = *txID
	}

	s.mu.RLock()
	tx := s.transactions[id]
	isSeqAndActive := tx != nil && tx.Sequential && tx.State == TxActive
	s.mu.RUnlock()

	if isSeqAndActive {
		s.mu.Lock()
		tx = s.transactions[id]
		if tx != nil && tx.Sequential && tx.State == TxActive {
			tx.ReadSet[key] = struct{}{}
		}
		s.mu.Unlock()
	}

	// Try cache first
	if rec, ok := s.cache.Get(key); ok {
		if s.isVisible(id, rec.TransactionID) {
			return rec.Value, rec.TransactionID, nil
		}
	}

	// Search pending
	s.mu.RLock()
	var valBytes []byte
	var foundTxID uint64
	var found bool

	if indices, ok := s.pendingIndex[key]; ok {
		for i := len(indices) - 1; i >= 0; i-- {
			rec := s.pendingRecords[indices[i]]
			if s.isVisibleLocked(id, rec.TransactionID) {
				valBytes = rec.Value
				foundTxID = rec.TransactionID
				found = true
				break
			}
		}
	}
	if !found && s.isMerging {
		if indices, ok := s.mergingIndex[key]; ok {
			for i := len(indices) - 1; i >= 0; i-- {
				rec := s.mergingRecords[indices[i]]
				if s.isVisibleLocked(id, rec.TransactionID) {
					valBytes = rec.Value
					foundTxID = rec.TransactionID
					found = true
					break
				}
			}
		}
	}
	rootAddr := s.bTreeRoot
	s.mu.RUnlock()

	if found {
		return valBytes, foundTxID, nil
	}

	// Search history in BTree
	history, _, err := s.btree.SearchHistory(ctx, rootAddr, key, 0, id, 100)
	if err != nil {
		return nil, 0, err
	}
	for _, val := range history {
		if s.isVisible(id, val.TransactionID) {
			return val.Value, val.TransactionID, nil
		}
	}

	return nil, 0, fmt.Errorf("key not found: %s", key)
}

// BatchPut adds multiple key-value pairs at once.
func (s *FileKeyValueStore) BatchPut(ctx context.Context, txID *uint64, kvs map[string][]byte) (uint64, error) {
	var id uint64
	var err error
	implicit := false
	if txID == nil {
		id, err = s.StartTransaction(ctx, false)
		if err != nil {
			return 0, err
		}
		implicit = true
		txID = &id
	}

	for key, val := range kvs {
		_, err := s.Put(ctx, txID, key, val)
		if err != nil {
			return 0, err
		}
	}

	if implicit {
		err = s.CommitTransaction(ctx, id)
		if err != nil {
			return 0, err
		}
	}
	return *txID, nil
}

// BatchGet fetches the values for multiple keys at once.
func (s *FileKeyValueStore) BatchGet(ctx context.Context, txID *uint64, keys []string) (map[string]ValueWithTransaction, error) {
	var id uint64
	if txID == nil {
		chk, err := s.CreateCheckpoint(ctx)
		if err != nil {
			return nil, err
		}
		id = chk
		txID = &id
	}

	results := make(map[string]ValueWithTransaction)
	for _, key := range keys {
		val, tID, err := s.Get(ctx, txID, key)
		if err == nil && val != nil {
			results[key] = ValueWithTransaction{
				Value:         val,
				TransactionID: tID,
			}
		}
	}
	return results, nil
}

// GetHistory retrieves historical values for a key.
func (s *FileKeyValueStore) GetHistory(ctx context.Context, txID *uint64, key string, minTxID uint64, maxTxID uint64, pageSize int) (HistoryPage, error) {
	var id uint64
	if txID == nil {
		chk, err := s.CreateCheckpoint(ctx)
		if err != nil {
			return HistoryPage{}, err
		}
		id = chk
		txID = &id
	}

	var page HistoryPage

	s.mu.RLock()
	tx := s.transactions[id]
	if tx != nil && tx.Sequential && tx.State == TxActive {
		s.mu.RUnlock()
		s.mu.Lock()
		tx = s.transactions[id]
		if tx != nil && tx.Sequential && tx.State == TxActive {
			tx.ReadSet[key] = struct{}{}
		}
		s.mu.Unlock()
		s.mu.RLock()
	}

	if indices, ok := s.pendingIndex[key]; ok {
		for i := len(indices) - 1; i >= 0; i-- {
			rec := s.pendingRecords[indices[i]]
			if rec.TransactionID <= maxTxID && rec.TransactionID >= minTxID && s.isVisibleLocked(id, rec.TransactionID) {
				page.Values = append(page.Values, ValueWithTransaction{
					Value:         rec.Value,
					TransactionID: rec.TransactionID,
				})
				if len(page.Values) >= pageSize {
					page.HasMore = true
					s.mu.RUnlock()
					return page, nil
				}
			}
		}
	}
	if s.isMerging {
		if indices, ok := s.mergingIndex[key]; ok {
			for i := len(indices) - 1; i >= 0; i-- {
				rec := s.mergingRecords[indices[i]]
				if rec.TransactionID <= maxTxID && rec.TransactionID >= minTxID && s.isVisibleLocked(id, rec.TransactionID) {
					page.Values = append(page.Values, ValueWithTransaction{
						Value:         rec.Value,
						TransactionID: rec.TransactionID,
					})
					if len(page.Values) >= pageSize {
						page.HasMore = true
						s.mu.RUnlock()
						return page, nil
					}
				}
			}
		}
	}
	rootAddr := s.bTreeRoot
	s.mu.RUnlock()

	remaining := pageSize - len(page.Values)
	if remaining > 0 {
		// B-Tree search needs to fetch more because we might filter out non-visible ones
		btreeVals, _, err := s.btree.SearchHistory(ctx, rootAddr, key, minTxID, maxTxID, remaining+100)
		if err != nil {
			return page, err
		}
		for _, val := range btreeVals {
			if s.isVisible(id, val.TransactionID) {
				page.Values = append(page.Values, val)
				if len(page.Values) >= pageSize {
					page.HasMore = true
					break
				}
			}
		}
	}

	return page, nil
}

// BatchGetHistory fetches historical values for multiple keys at once.
func (s *FileKeyValueStore) BatchGetHistory(ctx context.Context, txID *uint64, keys []string, minTxID uint64, maxTxID uint64, pageSize int) (map[string]HistoryPage, error) {
	var id uint64
	if txID == nil {
		chk, err := s.CreateCheckpoint(ctx)
		if err != nil {
			return nil, err
		}
		id = chk
		txID = &id
	}

	results := make(map[string]HistoryPage)
	for _, key := range keys {
		page, err := s.GetHistory(ctx, txID, key, minTxID, maxTxID, pageSize)
		if err == nil && len(page.Values) > 0 {
			results[key] = page
		}
	}
	return results, nil
}
