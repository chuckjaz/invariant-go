package kv

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	// Group commit coordination
	syncEpoch   uint64
	syncedEpoch uint64
	syncing     bool
	syncCond    *sync.Cond

	// Asynchronous background uploads
	uploadCh chan string
	uploadWg sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
}

func NewJournal(baseDir string, store storage.Storage, slotClient slots.Slots, slotID string, slotAuth []byte, previousJournal *content.ContentLink, maxEntries int, opts content.WriterOptions) (*Journal, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &Journal{
		baseDir:         baseDir,
		storage:         store,
		slotClient:      slotClient,
		slotID:          slotID,
		slotAuth:        slotAuth,
		previousJournal: previousJournal,
		maxEntries:      maxEntries,
		opts:            opts,
		uploadCh:        make(chan string, 500),
		ctx:             ctx,
		cancel:          cancel,
	}
	j.syncCond = sync.NewCond(&j.mu)
	if err := j.openNewFile(); err != nil {
		cancel()
		return nil, err
	}
	go j.processUploads()
	return j, nil
}

func (j *Journal) processUploads() {
	for {
		select {
		case <-j.ctx.Done():
			// Drain any remaining uploaded files before exiting
			for {
				select {
				case path := <-j.uploadCh:
					j.uploadSingleJournal(path)
					j.uploadWg.Done()
				default:
					return
				}
			}
		case path, ok := <-j.uploadCh:
			if !ok {
				return
			}
			j.uploadSingleJournal(path)
			j.uploadWg.Done()
		}
	}
}

func (j *Journal) uploadSingleJournal(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	j.mu.Lock()
	prev := j.previousJournal
	j.mu.Unlock()

	// Prepend header to file content for storage upload
	var headerBuf bytes.Buffer
	header := JournalEntry{Header: &JournalHeader{PreviousJournal: prev}}
	if err := json.NewEncoder(&headerBuf).Encode(header); err != nil {
		return
	}

	multiReader := io.MultiReader(&headerBuf, file)
	link, err := content.Write(multiReader, j.storage, j.opts)
	if err != nil {
		return
	}

	linkBytes, err := json.Marshal(link)
	if err != nil {
		return
	}
	newJournalStr := string(linkBytes)

	if prev == nil {
		err = j.slotClient.Create(context.Background(), j.slotID, newJournalStr, "")
		if err != nil {
			currVal, _ := j.slotClient.Get(context.Background(), j.slotID)
			err = j.slotClient.Update(context.Background(), j.slotID, newJournalStr, currVal, j.slotAuth)
		}
	} else {
		oldLinkBytes, _ := json.Marshal(prev)
		err = j.slotClient.Update(context.Background(), j.slotID, newJournalStr, string(oldLinkBytes), j.slotAuth)
	}

	if err == nil {
		j.mu.Lock()
		j.previousJournal = &link
		j.mu.Unlock()
		os.Remove(path)
	}
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
	j.syncEpoch = 0
	j.syncedEpoch = 0
	j.syncing = false

	return nil
}

// Append writes a new record to the local journal. Returns true if it was rotated.
// Crucial: For transactional commit records, this triggers a physical disk flush
// (via file.Sync() with group commit batching) before returning to guarantee ACID durability.
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

	// ACID Durability Rule: Sync to disk for commit or checkpoint records using Group Commit.
	if rec.Type == RecordTypeTxCommit || rec.Type == RecordTypeTxCheckpoint {
		if err := j.currentWriter.Flush(); err != nil {
			return false, err
		}
		myEpoch := j.syncEpoch + 1
		j.syncEpoch = myEpoch

		// Wait while another goroutine is actively syncing
		for j.syncing && j.syncedEpoch < myEpoch {
			j.syncCond.Wait()
		}

		if j.syncedEpoch < myEpoch {
			j.syncing = true
			targetFile := j.currentFile
			targetEpoch := j.syncEpoch
			j.mu.Unlock()

			// Perform physical disk sync without holding j.mu
			syncErr := targetFile.Sync()

			j.mu.Lock()
			j.syncing = false
			if syncErr == nil {
				if targetEpoch > j.syncedEpoch {
					j.syncedEpoch = targetEpoch
				}
			}
			j.syncCond.Broadcast()

			if syncErr != nil {
				return false, syncErr
			}
		}
	}

	j.lastRecordType = rec.Type
	j.entries++
	if j.entries >= j.maxEntries {
		if err := j.rotateLocked(); err != nil {
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

// rotateLocked rotates the local active journal file in microseconds and queues background upload.
func (j *Journal) rotateLocked() error {
	if j.currentFile == nil {
		return nil
	}
	_ = j.currentWriter.Flush()
	j.currentFile.Close()
	rotatedPath := j.currentPath

	if err := j.openNewFile(); err != nil {
		return err
	}

	j.uploadWg.Add(1)
	j.uploadCh <- rotatedPath
	return nil
}

// Flush closes the current journal, queues it for upload, and waits for all uploads to finish.
func (j *Journal) Flush(ctx context.Context) error {
	j.mu.Lock()
	if j.currentFile != nil && j.entries > 0 {
		if err := j.rotateLocked(); err != nil {
			j.mu.Unlock()
			return err
		}
	}
	j.mu.Unlock()

	j.uploadWg.Wait()
	return nil
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cancel != nil {
		j.cancel()
	}
	if j.currentFile != nil {
		_ = j.currentWriter.Flush()
		return j.currentFile.Close()
	}
	return nil
}

func (j *Journal) PreviousJournal() *content.ContentLink {
	j.uploadWg.Wait()
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
	// Wait for any pending uploads to ensure remote storage is up to date
	j.uploadWg.Wait()

	j.mu.Lock()
	curr := j.previousJournal
	j.mu.Unlock()

	var pages [][]Record

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
