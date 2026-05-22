package kv

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"invariant/internal/content"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

type JournalHeader struct {
	PreviousJournal *content.ContentLink `json:"prev,omitempty"`
}

type JournalEntry struct {
	Header *JournalHeader `json:"h,omitempty"`
	Record *Record        `json:"r,omitempty"`
}

type Journal struct {
	baseDir         string
	storage         storage.Storage
	slotClient      slots.Slots
	slotID          string
	slotAuth        []byte
	currentFile     *os.File
	currentPath     string
	currentEncoder  *json.Encoder
	previousJournal *content.ContentLink
	entries         int
	maxEntries      int
	opts            content.WriterOptions
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
	j.currentEncoder = json.NewEncoder(file)
	j.entries = 0

	// Write header
	header := JournalEntry{Header: &JournalHeader{PreviousJournal: j.previousJournal}}
	if err := j.currentEncoder.Encode(header); err != nil {
		file.Close()
		return err
	}
	return file.Sync()
}

// Append writes a new record to the local journal. Returns true if it was flushed.
func (j *Journal) Append(ctx context.Context, rec Record) (bool, error) {
	entry := JournalEntry{Record: &rec}
	if err := j.currentEncoder.Encode(entry); err != nil {
		return false, err
	}
	if err := j.currentFile.Sync(); err != nil {
		return false, err
	}

	j.entries++
	if j.entries >= j.maxEntries {
		if err := j.Flush(ctx); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// Flush closes the current journal, uploads it to storage, updates the previousJournal pointer, and opens a new file.
func (j *Journal) Flush(ctx context.Context) error {
	if j.currentFile == nil {
		return nil
	}
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

func (j *Journal) Close() error {
	if j.currentFile != nil {
		return j.currentFile.Close()
	}
	return nil
}

func (j *Journal) PreviousJournal() *content.ContentLink {
	return j.previousJournal
}

// SetPreviousJournal updates the journal pointer, used after B-tree merge.
func (j *Journal) SetPreviousJournal(link *content.ContentLink) {
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
		if !e.IsDir() {
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
