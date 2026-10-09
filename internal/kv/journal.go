package kv

import (
	"context"
	"fmt"
	"os"
	"sync"

	"invariant/internal/content"
	chainedjournal "invariant/internal/journal/chained"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

type RecordType int

const (
	RecordTypePut RecordType = iota
	RecordTypeTxStart
	RecordTypeTxCommit
	RecordTypeTxAbort
	RecordTypeTxCheckpoint
)

type Record struct {
	Type          RecordType `json:"t"`
	Key           string     `json:"k,omitempty"`
	TransactionID uint64     `json:"tx"`
	Value         []byte     `json:"v,omitempty"`
	Sequential    bool       `json:"seq,omitempty"`
	UserID        string     `json:"u,omitempty"`
}

// Journal represents a write-ahead journal for durability backed by ChainedJournal.
type Journal struct {
	cj             *chainedjournal.ChainedJournal[Record]
	baseDir        string
	currentFile    *os.File
	mu             sync.Mutex
	lastRecordType RecordType
	closed         bool
}

func NewJournal(baseDir string, store storage.Storage, slotClient slots.Slots, slotID string, slotAuth []byte, previousJournal *content.ContentLink, maxEntries int, opts content.WriterOptions) (*Journal, error) {
	cj, err := chainedjournal.NewChainedJournal[Record](chainedjournal.ChainedJournalOptions{
		BaseDir:         baseDir,
		Storage:         store,
		Slots:           slotClient,
		SlotID:          slotID,
		SlotAuth:        slotAuth,
		PreviousJournal: previousJournal,
		MaxEntries:      maxEntries,
		WriterOpts:      opts,
	})
	if err != nil {
		return nil, err
	}
	return &Journal{
		cj:          cj,
		baseDir:     baseDir,
		currentFile: os.NewFile(0, "active"),
	}, nil
}

// Append writes a new record to the local journal. Returns true if it was rotated.
// For transactional commit and checkpoint records, it physically flushes to disk using group commit.
func (j *Journal) Append(ctx context.Context, rec Record) (bool, error) {
	if whois, ok := WhoIsFromContext(ctx); ok && whois.UserProfile != nil {
		rec.UserID = whois.UserProfile.ID.String()
	}

	syncToDisk := rec.Type == RecordTypeTxCommit || rec.Type == RecordTypeTxCheckpoint
	flushed, err := j.cj.Append(ctx, rec, syncToDisk)
	if err != nil {
		return false, err
	}

	j.mu.Lock()
	j.lastRecordType = rec.Type
	j.mu.Unlock()

	return flushed, nil
}

// LastRecordType returns the type of the most recently appended record in the current active journal.
func (j *Journal) LastRecordType() RecordType {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lastRecordType
}

// Flush closes the current journal, queues it for upload, and waits for all uploads to finish.
func (j *Journal) Flush(ctx context.Context) error {
	return j.cj.Flush(ctx)
}

// Close closes the journal and flushes any pending writes.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.closed {
		if j.currentFile == nil {
			return nil
		}
		return fmt.Errorf("journal already closed")
	}

	j.closed = true
	if j.cj != nil {
		return j.cj.Close()
	}
	return nil
}

// PreviousJournal returns the link to the most recently uploaded journal page.
func (j *Journal) PreviousJournal() *content.ContentLink {
	return j.cj.PreviousJournal()
}

// SetPreviousJournal updates the journal pointer, used after B-tree merge.
func (j *Journal) SetPreviousJournal(link *content.ContentLink) {
	j.cj.SetPreviousJournal(link)
}

// LoadLocalJournals reads all local un-flushed journals.
func (j *Journal) LoadLocalJournals() ([]Record, error) {
	if j.cj != nil {
		return j.cj.LoadLocalEntries()
	}
	if j.baseDir != "" {
		_, err := os.ReadDir(j.baseDir)
		if err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// LoadRemoteJournals reads journal entries from storage, starting at the current previousJournal
// and going backward until it hits stopAt (or nil). It returns the records in chronological order.
func (j *Journal) LoadRemoteJournals(ctx context.Context, stopAt *content.ContentLink) ([]Record, error) {
	return j.cj.LoadRemoteEntries(ctx, stopAt)
}
