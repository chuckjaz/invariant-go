package chainedjournal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chuckjaz/invariant-go/internal/content"
	"github.com/chuckjaz/invariant-go/internal/slots"
	"github.com/chuckjaz/invariant-go/internal/storage"
)

// ChainedHeader stores the pointer to the previous journal page in CAS storage.
type ChainedHeader struct {
	PreviousJournal *content.ContentLink `json:"prev,omitempty"`
}

// ChainedEntry encapsulates either a header or a data item in the journal stream.
type ChainedEntry[T any] struct {
	Header *ChainedHeader `json:"h,omitempty"`
	Record *T             `json:"r,omitempty"`
}

// ChainedJournalOptions configures a ChainedJournal.
type ChainedJournalOptions struct {
	BaseDir         string
	Storage         storage.Storage
	Slots           slots.Slots
	SlotID          string
	SlotAuth        []byte
	PreviousJournal *content.ContentLink
	MaxEntries      int
	WriterOpts      content.WriterOptions
}

// ChainedJournal provides a write-ahead journal buffered locally to disk,
// periodically uploaded to a CAS storage service with previous block address chaining,
// and synchronized to a slot.
type ChainedJournal[T any] struct {
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

// NewChainedJournal creates a new ChainedJournal.
func NewChainedJournal[T any](opts ChainedJournalOptions) (*ChainedJournal[T], error) {
	if err := os.MkdirAll(opts.BaseDir, 0755); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	previousJournal := opts.PreviousJournal
	if previousJournal == nil && opts.Slots != nil && opts.SlotID != "" {
		slotVal, err := opts.Slots.Get(context.Background(), opts.SlotID)
		if err == nil && slotVal != "" {
			var link content.ContentLink
			if err := json.Unmarshal([]byte(slotVal), &link); err == nil {
				previousJournal = &link
			}
		}
	}

	j := &ChainedJournal[T]{
		baseDir:         opts.BaseDir,
		storage:         opts.Storage,
		slotClient:      opts.Slots,
		slotID:          opts.SlotID,
		slotAuth:        opts.SlotAuth,
		previousJournal: previousJournal,
		maxEntries:      opts.MaxEntries,
		opts:            opts.WriterOpts,
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

func (j *ChainedJournal[T]) processUploads() {
	for {
		select {
		case <-j.ctx.Done():
			// Drain remaining files before exiting
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

func (j *ChainedJournal[T]) uploadSingleJournal(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	if j.storage == nil {
		// No remote storage configured, simply remove the rotated file
		os.Remove(path)
		return
	}

	j.mu.Lock()
	prev := j.previousJournal
	j.mu.Unlock()

	var headerBuf bytes.Buffer
	header := ChainedEntry[T]{Header: &ChainedHeader{PreviousJournal: prev}}
	if err := json.NewEncoder(&headerBuf).Encode(header); err != nil {
		return
	}

	multiReader := io.MultiReader(&headerBuf, file)
	link, err := content.Write(multiReader, j.storage, j.opts)
	if err != nil {
		return
	}

	if j.slotClient != nil && j.slotID != "" {
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
		if err != nil {
			return
		}
	}

	j.mu.Lock()
	j.previousJournal = &link
	j.mu.Unlock()
	os.Remove(path)
}

var fileSeq atomic.Uint64

func (j *ChainedJournal[T]) openNewFile() error {
	name := fmt.Sprintf("journal-%d-%d.jsonl", time.Now().UnixNano(), fileSeq.Add(1))
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

// Append writes a new record to the local journal. If syncToDisk is true,
// it flushes and physically syncs to disk using group commit.
// Returns true if the journal rotated.
func (j *ChainedJournal[T]) Append(ctx context.Context, item T, syncToDisk bool) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	entry := ChainedEntry[T]{Record: &item}
	if err := j.currentEncoder.Encode(entry); err != nil {
		return false, err
	}

	if syncToDisk {
		if err := j.currentWriter.Flush(); err != nil {
			return false, err
		}
		myEpoch := j.syncEpoch + 1
		j.syncEpoch = myEpoch

		for j.syncing && j.syncedEpoch < myEpoch {
			j.syncCond.Wait()
		}

		if j.syncedEpoch < myEpoch {
			j.syncing = true
			targetFile := j.currentFile
			targetEpoch := j.syncEpoch
			j.mu.Unlock()

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

	j.entries++
	if j.maxEntries > 0 && j.entries >= j.maxEntries {
		if err := j.rotateLocked(); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (j *ChainedJournal[T]) rotateLocked() error {
	for j.syncing {
		j.syncCond.Wait()
	}
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

// Rotate forces a rotation of the current active journal file.
func (j *ChainedJournal[T]) Rotate(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.rotateLocked()
}

// Flush closes the current journal, queues it for upload, and waits for all uploads to finish.
func (j *ChainedJournal[T]) Flush(ctx context.Context) error {
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

// Sync physically flushes the current in-memory buffer to disk.
func (j *ChainedJournal[T]) Sync() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.currentFile != nil {
		if err := j.currentWriter.Flush(); err != nil {
			return err
		}
		return j.currentFile.Sync()
	}
	return nil
}

// Close closes the journal and waits for background tasks to finish.
func (j *ChainedJournal[T]) Close() error {
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

// PreviousJournal returns the link to the most recently uploaded journal page.
func (j *ChainedJournal[T]) PreviousJournal() *content.ContentLink {
	j.uploadWg.Wait()
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.previousJournal
}

// SetPreviousJournal manually updates the previous journal pointer.
func (j *ChainedJournal[T]) SetPreviousJournal(link *content.ContentLink) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.previousJournal = link
}

// LoadLocalEntries reads all local un-flushed journal entries from disk.
func (j *ChainedJournal[T]) LoadLocalEntries() ([]T, error) {
	entries, err := os.ReadDir(j.baseDir)
	if err != nil {
		return nil, err
	}
	var records []T
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "journal-") && strings.HasSuffix(e.Name(), ".jsonl") {
			path := filepath.Join(j.baseDir, e.Name())
			file, err := os.Open(path)
			if err != nil {
				continue
			}
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				var entry ChainedEntry[T]
				if err := json.Unmarshal(scanner.Bytes(), &entry); err == nil && entry.Record != nil {
					records = append(records, *entry.Record)
				}
			}
			file.Close()
		}
	}
	return records, nil
}

// LoadRemoteEntries reads journal entries from storage, starting at the current previousJournal
// and going backward until it hits stopAt (or nil). It returns the records in chronological order.
func (j *ChainedJournal[T]) LoadRemoteEntries(ctx context.Context, stopAt *content.ContentLink) ([]T, error) {
	j.uploadWg.Wait()

	j.mu.Lock()
	curr := j.previousJournal
	j.mu.Unlock()

	if curr == nil && j.slotClient != nil && j.slotID != "" {
		slotVal, err := j.slotClient.Get(ctx, j.slotID)
		if err == nil && slotVal != "" {
			var link content.ContentLink
			if err := json.Unmarshal([]byte(slotVal), &link); err == nil {
				curr = &link
				j.mu.Lock()
				j.previousJournal = curr
				j.mu.Unlock()
			}
		}
	}

	var pages [][]T

	for curr != nil {
		if stopAt != nil && curr.Address == stopAt.Address {
			break
		}

		rc, err := content.Read(*curr, j.storage, j.slotClient)
		if err != nil {
			return nil, err
		}

		scanner := bufio.NewScanner(rc)
		var pageRecs []T
		var prevLink *content.ContentLink

		for scanner.Scan() {
			var entry ChainedEntry[T]
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

	var records []T
	for _, page := range slices.Backward(pages) {
		records = append(records, page...)
	}
	return records, nil
}
