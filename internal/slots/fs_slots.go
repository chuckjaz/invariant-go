// Package slots provides the file system implementation for the slots service.
package slots

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var _ Slots = (*FileSystemSlots)(nil)

// FileSystemSlots provides a file system-backed implementation of the Slots interface.
type FileSystemSlots struct {
	id               string
	mu               sync.RWMutex
	store            map[string]string
	baseDir          string
	journalFile      *os.File
	journalName      string
	snapshotInterval time.Duration
	stopCh           chan struct{}
}

type journalEntry struct {
	Op      string `json:"op"` // "POST" (create) or "PUT" (update)
	ID      string `json:"id"`
	Address string `json:"address"`
}

// NewFileSystemSlots creates a new FileSystemSlots instance.
func NewFileSystemSlots(baseDir string, snapshotInterval time.Duration) (*FileSystemSlots, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, err
	}

	idPath := filepath.Join(baseDir, "id")
	var id string
	if data, err := os.ReadFile(idPath); err == nil && len(data) == 64 {
		id = string(data)
	} else {
		idBytes := make([]byte, 32)
		rand.Read(idBytes)
		id = hex.EncodeToString(idBytes)
		os.WriteFile(idPath, []byte(id), 0644)
	}

	fs := &FileSystemSlots{
		id:               id,
		store:            make(map[string]string),
		baseDir:          baseDir,
		snapshotInterval: snapshotInterval,
		stopCh:           make(chan struct{}),
	}

	// 1. Load snapshot
	snapshotPath := filepath.Join(baseDir, "snapshot.json")
	if data, err := os.ReadFile(snapshotPath); err == nil {
		if err := json.Unmarshal(data, &fs.store); err != nil {
			return nil, fmt.Errorf("failed to unmarshal snapshot: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read snapshot: %w", err)
	}

	// 2. Load journals
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, err
	}

	var journals []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "journal-") && strings.HasSuffix(entry.Name(), ".jsonl") {
			journals = append(journals, entry.Name())
		}
	}
	sort.Strings(journals)

	for _, journal := range journals {
		if err := fs.applyJournal(filepath.Join(baseDir, journal)); err != nil {
			return nil, fmt.Errorf("failed to apply journal %s: %w", journal, err)
		}
	}

	// 3. Open new journal
	if err := fs.openNewJournal(); err != nil {
		return nil, err
	}

	// 4. Start background snapshot goroutine
	if snapshotInterval > 0 {
		go fs.snapshotLoop()
	}

	return fs, nil
}

// ID returns the service ID.
func (s *FileSystemSlots) ID() string {
	return s.id
}

func (s *FileSystemSlots) applyJournal(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry journalEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // Skip malformed lines
		}
		switch entry.Op {
		case "POST", "PUT":
			s.store[entry.ID] = entry.Address
		}
	}
	return scanner.Err()
}

func (s *FileSystemSlots) openNewJournal() error {
	name := fmt.Sprintf("journal-%d.jsonl", time.Now().UnixNano())
	path := filepath.Join(s.baseDir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	if s.journalFile != nil {
		s.journalFile.Close()
	}

	s.journalFile = file
	s.journalName = name
	return nil
}

// Close closes the file system slots, stopping the snapshot loop and closing the journal file.
func (s *FileSystemSlots) Close() error {
	close(s.stopCh)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.journalFile != nil {
		err := s.journalFile.Close()
		s.journalFile = nil
		return err
	}
	return nil
}

// Get returns the address for the given slot ID.
func (s *FileSystemSlots) Get(id string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	addr, ok := s.store[id]
	if !ok {
		return "", ErrSlotNotFound
	}

	return addr, nil
}

// Create creates a new slot with the given address.
func (s *FileSystemSlots) Create(id string, address string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.store[id]; exists {
		return ErrSlotExists
	}

	entry := journalEntry{
		Op:      "POST",
		ID:      id,
		Address: address,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if _, err := s.journalFile.Write(data); err != nil {
		return err
	}
	if err := s.journalFile.Sync(); err != nil {
		return err
	}

	s.store[id] = address
	return nil
}

// Update attempts to change the address of a slot, ensuring the previous address matches.
func (s *FileSystemSlots) Update(id string, address string, previousAddress string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	currentAddr, ok := s.store[id]
	if !ok {
		return ErrSlotNotFound
	}
	if currentAddr != previousAddress {
		return ErrConflict
	}

	entry := journalEntry{
		Op:      "PUT",
		ID:      id,
		Address: address,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if _, err := s.journalFile.Write(data); err != nil {
		return err
	}
	if err := s.journalFile.Sync(); err != nil {
		return err
	}

	s.store[id] = address
	return nil
}

func (s *FileSystemSlots) snapshotLoop() {
	ticker := time.NewTicker(s.snapshotInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.doSnapshot()
		}
	}
}

func (s *FileSystemSlots) doSnapshot() {
	s.mu.Lock()
	// Copy the map to safely marshal it outside the lock
	storeCopy := make(map[string]string, len(s.store))
	for k, v := range s.store {
		storeCopy[k] = v
	}

	// Create new journal while holding the lock
	if err := s.openNewJournal(); err != nil {
		s.mu.Unlock()
		return
	}
	newJournal := s.journalName
	s.mu.Unlock()

	// 1. Write the copied store to a temporary snapshot file
	tmpPath := filepath.Join(s.baseDir, "snapshot.tmp")
	file, err := os.Create(tmpPath)
	if err != nil {
		return
	}

	if err := json.NewEncoder(file).Encode(storeCopy); err != nil {
		file.Close()
		os.Remove(tmpPath)
		return
	}

	// Fsync before close
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tmpPath)
		return
	}
	if err := file.Close(); err != nil {
		os.Remove(tmpPath)
		return
	}

	// 2. Rename the temporary snapshot to the actual snapshot
	finalPath := filepath.Join(s.baseDir, "snapshot.json")
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return
	}

	// 3. Safely remove old journals
	entries, err := os.ReadDir(s.baseDir)
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasPrefix(entry.Name(), "journal-") && strings.HasSuffix(entry.Name(), ".jsonl") {
				if entry.Name() != newJournal {
					os.Remove(filepath.Join(s.baseDir, entry.Name()))
				}
			}
		}
	}
}
