package kv

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"invariant/internal/content"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

type JournalHeader struct {
	PreviousJournal *content.ContentLink `json:"prev,omitempty"`
}

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

type JournalEntry struct {
	Header *JournalHeader `json:"h,omitempty"`
	Record *Record        `json:"r,omitempty"`
}

// Journal represents a write-ahead journal for durability.
// ACID Durability Rule: Whenever a transaction is committed, all associated updates
// and the commit record must be successfully written and synchronized to physical disk
// storage to ensure the changes survive unexpected crashes or power failures.
type Journal struct {
	mu              sync.Mutex
	baseDir         string
	storage         storage.Storage
	slotClient      slots.Slots
	slotID          string
	slotAuth        []byte
	currentFile     *os.File
	currentWriter   *bufio.Writer
	currentPath     string
	currentEncoder  *json.Encoder
	previousJournal *content.ContentLink
	entries         int
	maxEntries      int
	opts            content.WriterOptions
	lastRecordType  RecordType
}

func NewJournal(baseDir string, store storage.Storage, slotClient slots.Slots, slotID string, slotAuth []byte, previousJournal *content.ContentLink, maxEntries int, opts content.WriterOptions) (*Journal, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, err
	}
	j := &Journal{
		baseDir:         baseDir,
		storage:         store,
		slotClient:      slotClient,
		slotID:          slotID,
		slotAuth:        slotAuth,
		previousJournal: previousJournal,
		maxEntries:      maxEntries,
		opts:            opts,
	}
	if err := j.openNewFile(); err != nil {
		return nil, err
	}
	return j, nil
}

func (j *Journal) openNewFile() error {
	name := fmt.Sprintf("journal-%d.jsonl", time.Now().UnixNano())
	path := filepath.Join(j.baseDir, name)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	j.currentFile = file
	j.currentPath = path
	j.currentWriter = bufio.NewWriter(file)
	j.currentEncoder = json.NewEncoder(j.currentWriter)
	j.entries = 0

	// Write header
	header := JournalEntry{Header: &JournalHeader{PreviousJournal: j.previousJournal}}
	if err := j.currentEncoder.Encode(header); err != nil {
		file.Close()
		return err
	}
	if err := j.currentWriter.Flush(); err != nil {
		file.Close()
		return err
	}
	return file.Sync()
}

// Append writes a new record to the local journal. Returns true if it was flushed.
// Crucial: For transactional commit records, this must trigger a physical disk flush
// (via file.Sync()) before returning to guarantee ACID durability.
func (j *Journal) Append(ctx context.Context, rec Record) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if whois, ok := WhoIsFromContext(ctx); ok && whois.UserProfile != nil {
		rec.UserID = whois.UserProfile.ID.String()
	}

	entry := JournalEntry{Record: &rec}
	if err := j.currentEncoder.Encode(entry); err != nil {
		return false, err
	}
	// ACID Durability Rule: Only sync to disk synchronously for commit or checkpoint records.
	// This ensures previously buffered updates are safely persisted at commit time.
	if rec.Type == RecordTypeTxCommit || rec.Type == RecordTypeTxCheckpoint {
		if err := j.currentWriter.Flush(); err != nil {
			return false, err
		}
		if err := j.currentFile.Sync(); err != nil {
			return false, err
		}
	}

	j.lastRecordType = rec.Type
	j.entries++
	if j.entries >= j.maxEntries {
		if err := j.flushLocked(ctx); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// LastRecordType returns the type of the most recently appended record in the current active journal.
func (j *Journal) LastRecordType() RecordType {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lastRecordType
}

// flushLocked performs the actual flush, assumes j.mu is held.
func (j *Journal) flushLocked(ctx context.Context) error {
	if j.currentFile == nil {
		return nil
	}
	_ = j.currentWriter.Flush()
	j.currentFile.Close()

	// Read and upload to storage
	data, err := os.ReadFile(j.currentPath)
	if err != nil {
		return err
	}

	link, err := content.Write(bytes.NewReader(data), j.storage, j.opts)
	if err != nil {
		return err
	}

	linkBytes, err := json.Marshal(link)
	if err != nil {
		return err
	}
	newJournalStr := string(linkBytes)

	if j.previousJournal == nil {
		err = j.slotClient.Create(ctx, j.slotID, newJournalStr, "")
	} else {
		oldLinkBytes, _ := json.Marshal(j.previousJournal)
		err = j.slotClient.Update(ctx, j.slotID, newJournalStr, string(oldLinkBytes), j.slotAuth)
	}
	if err != nil {
		return err
	}

	j.previousJournal = &link

	// Delete the local file since it's uploaded
	os.Remove(j.currentPath)

	return j.openNewFile()
}

// Flush closes the current journal, uploads it to storage, updates the previousJournal pointer, and opens a new file.
func (j *Journal) Flush(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.flushLocked(ctx)
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.currentFile != nil {
		_ = j.currentWriter.Flush()
		return j.currentFile.Close()
	}
	return nil
}

func (j *Journal) PreviousJournal() *content.ContentLink {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.previousJournal
}

// SetPreviousJournal updates the journal pointer, used after B-tree merge.
func (j *Journal) SetPreviousJournal(link *content.ContentLink) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.previousJournal = link
}

// LoadLocalJournals reads all local un-flushed journals.
func (j *Journal) LoadLocalJournals() ([]Record, error) {
	entries, err := os.ReadDir(j.baseDir)
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "journal-") && strings.HasSuffix(e.Name(), ".jsonl") {
			path := filepath.Join(j.baseDir, e.Name())
			file, err := os.Open(path)
			if err != nil {
				continue
			}
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				var entry JournalEntry
				if err := json.Unmarshal(scanner.Bytes(), &entry); err == nil && entry.Record != nil {
					records = append(records, *entry.Record)
				}
			}
			file.Close()
		}
	}
	return records, nil
}

// LoadRemoteJournals reads journal entries from storage, starting at the current previousJournal
// and going backward until it hits stopAt (or nil). It returns the records in chronological order.
func (j *Journal) LoadRemoteJournals(ctx context.Context, stopAt *content.ContentLink) ([]Record, error) {
	var pages [][]Record
	curr := j.previousJournal

	for curr != nil {
		if stopAt != nil && curr.Address == stopAt.Address {
			break
		}

		rc, err := content.Read(*curr, j.storage, j.slotClient)
		if err != nil {
			return nil, err
		}

		scanner := bufio.NewScanner(rc)
		var pageRecs []Record
		var prevLink *content.ContentLink

		for scanner.Scan() {
			var entry JournalEntry
			if err := json.Unmarshal(scanner.Bytes(), &entry); err == nil {
				if entry.Header != nil {
					prevLink = entry.Header.PreviousJournal
				}
				if entry.Record != nil {
					pageRecs = append(pageRecs, *entry.Record)
				}
			}
		}
		rc.Close()

		pages = append(pages, pageRecs)
		curr = prevLink
	}

	// Pages are from newest to oldest. We want chronological order (oldest to newest).
	var records []Record
	for i := len(pages) - 1; i >= 0; i-- {
		records = append(records, pages[i]...)
	}
	return records, nil
}
