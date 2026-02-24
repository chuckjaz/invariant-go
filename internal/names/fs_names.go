package names

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

	"invariant/internal/identity"
)

// Assert that FileSystemNames implements the Names interface
var _ Names = (*FileSystemNames)(nil)

// Assert that FileSystemNames implements the identity.Provider interface
var _ identity.Provider = (*FileSystemNames)(nil)

type FileSystemNames struct {
	id               string
	mu               sync.RWMutex
	store            map[string]NameEntry
	baseDir          string
	journalFile      *os.File
	journalName      string
	snapshotInterval time.Duration
	stopCh           chan struct{}
}

type journalEntry struct {
	Op     string   `json:"op"` // "PUT" or "DELETE"
	Name   string   `json:"name"`
	Value  string   `json:"value,omitempty"`
	Tokens []string `json:"tokens,omitempty"`
}

func NewFileSystemNames(baseDir string, snapshotInterval time.Duration) (*FileSystemNames, error) {
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

	fsn := &FileSystemNames{
		id:               id,
		store:            make(map[string]NameEntry),
		baseDir:          baseDir,
		snapshotInterval: snapshotInterval,
		stopCh:           make(chan struct{}),
	}

	// 1. Load snapshot
	snapshotPath := filepath.Join(baseDir, "snapshot.json")
	if data, err := os.ReadFile(snapshotPath); err == nil {
		if err := json.Unmarshal(data, &fsn.store); err != nil {
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
		if err := fsn.applyJournal(filepath.Join(baseDir, journal)); err != nil {
			return nil, fmt.Errorf("failed to apply journal %s: %w", journal, err)
		}
	}

	// 3. Open new journal
	if err := fsn.openNewJournal(); err != nil {
		return nil, err
	}

	// 4. Start background snapshot goroutine if interval > 0
	if snapshotInterval > 0 {
		go fsn.snapshotLoop()
	}

	return fsn, nil
}

func (s *FileSystemNames) ID() string {
	return s.id
}

func (s *FileSystemNames) applyJournal(path string) error {
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
		case "PUT":
			s.store[entry.Name] = NameEntry{Value: entry.Value, Tokens: entry.Tokens}
		case "DELETE":
			delete(s.store, entry.Name)
		}
	}
	return scanner.Err()
}

func (s *FileSystemNames) openNewJournal() error {
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

func (s *FileSystemNames) Close() error {
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

func (s *FileSystemNames) Get(name string) (NameEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.store[name]
	if !ok {
		return NameEntry{}, ErrNotFound
	}

	tokensCopy := make([]string, len(entry.Tokens))
	copy(tokensCopy, entry.Tokens)
	return NameEntry{
		Value:  entry.Value,
		Tokens: tokensCopy,
	}, nil
}

func (s *FileSystemNames) Put(name string, value string, tokens []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tokensCopy := make([]string, len(tokens))
	copy(tokensCopy, tokens)

	entry := journalEntry{
		Op:     "PUT",
		Name:   name,
		Value:  value,
		Tokens: tokensCopy,
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

	s.store[name] = NameEntry{
		Value:  value,
		Tokens: tokensCopy,
	}
	return nil
}

func (s *FileSystemNames) Delete(name string, expectedValue string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.store[name]
	if !ok {
		return ErrNotFound
	}

	if expectedValue != "" && entry.Value != expectedValue {
		// ETag mismatch
		return ErrPreconditionFailed
	}

	jEntry := journalEntry{
		Op:   "DELETE",
		Name: name,
	}
	data, err := json.Marshal(jEntry)
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

	delete(s.store, name)
	return nil
}

func (s *FileSystemNames) snapshotLoop() {
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

func (s *FileSystemNames) doSnapshot() {
	s.mu.Lock()
	// Copy the map to safely marshal it outside the lock
	storeCopy := make(map[string]NameEntry, len(s.store))
	for k, v := range s.store {
		tokensCopy := make([]string, len(v.Tokens))
		copy(tokensCopy, v.Tokens)
		storeCopy[k] = NameEntry{
			Value:  v.Value,
			Tokens: tokensCopy,
		}
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
