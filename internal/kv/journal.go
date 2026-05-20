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

	"invariant/internal/storage"
)

type JournalHeader struct {
	PreviousJournal string `json:"prev,omitempty"`
}

type JournalEntry struct {
	Header *JournalHeader `json:"h,omitempty"`
	Record *Record        `json:"r,omitempty"`
}

type Journal struct {
	baseDir         string
	storage         storage.Storage
	currentFile     *os.File
	currentPath     string
	currentEncoder  *json.Encoder
	previousJournal string
	entries         int
	maxEntries      int
}

func NewJournal(baseDir string, store storage.Storage, previousJournal string, maxEntries int) (*Journal, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, err
	}
	j := &Journal{
		baseDir:         baseDir,
		storage:         store,
		previousJournal: previousJournal,
		maxEntries:      maxEntries,
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

	addr, err := j.storage.Store(ctx, bytes.NewReader(data))
	if err != nil {
		return err
	}

	j.previousJournal = addr

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

func (j *Journal) PreviousJournal() string {
	return j.previousJournal
}

// SetPreviousJournal updates the journal pointer, used after B-tree merge.
func (j *Journal) SetPreviousJournal(addr string) {
	j.previousJournal = addr
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
