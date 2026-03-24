package journal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// entry represents an internal journal entry.
type entry[K comparable, V any] struct {
	Op    string `json:"op"`
	Key   K      `json:"key"`
	Value V      `json:"value,omitempty"`
}

// Store provides a file-system backed, append-only journaled map storage.
// K: The map key type.
// V: The map value type.
type Store[K comparable, V any] struct {
	mu               sync.RWMutex
	store            map[K]V
	baseDir          string
	journalFile      *os.File
	journalName      string
	snapshotInterval time.Duration
	stopCh           chan struct{}
}

// NewStore creates a new Store, loads the snapshot, applies the journals,
// and starts the background snapshot goroutine if snapshotInterval > 0.
func NewStore[K comparable, V any](
	baseDir string,
	snapshotInterval time.Duration,
) (*Store[K, V], error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, err
	}

	s := &Store[K, V]{
		store:            make(map[K]V),
		baseDir:          baseDir,
		snapshotInterval: snapshotInterval,
		stopCh:           make(chan struct{}),
	}

	// 1. Load snapshot
	snapshotPath := filepath.Join(baseDir, "snapshot.json")
	if data, err := os.ReadFile(snapshotPath); err == nil {
		if err := json.Unmarshal(data, &s.store); err != nil {
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

	for _, j := range journals {
		if err := s.applyJournal(filepath.Join(baseDir, j)); err != nil {
			return nil, fmt.Errorf("failed to apply journal %s: %w", j, err)
		}
	}

	// 3. Open new journal
	if err := s.openNewJournal(); err != nil {
		return nil, err
	}

	// 4. Start background snapshot goroutine
	if snapshotInterval > 0 {
		go s.snapshotLoop()
	}

	return s, nil
}

func (s *Store[K, V]) applyJournal(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var e entry[K, V]
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue // Skip malformed lines
		}
		switch e.Op {
		case "PUT":
			s.store[e.Key] = e.Value
		case "DELETE":
			delete(s.store, e.Key)
		}
	}
	return scanner.Err()
}

func (s *Store[K, V]) openNewJournal() error {
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

// Close gracefully stops the snapshot loop and closes the open journal file.
func (s *Store[K, V]) Close() error {
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

// Get safely retrieves a value from the store.
func (s *Store[K, V]) Get(k K) (V, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.store[k]
	return v, ok
}

// Read safely executes a callback that can read from the entire map.
func (s *Store[K, V]) Read(readFn func(store map[K]V)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	readFn(s.store)
}

// Put writes an update to the journal and the store, after passing checkFn (if provided)
func (s *Store[K, V]) Put(key K, value V, checkFn func(store map[K]V) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if checkFn != nil {
		if err := checkFn(s.store); err != nil {
			return err
		}
	}

	e := entry[K, V]{Op: "PUT", Key: key, Value: value}
	data, err := json.Marshal(e)
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

	s.store[key] = value
	return nil
}

// Delete removes an entry from the journal and the store, after passing checkFn (if provided)
func (s *Store[K, V]) Delete(key K, checkFn func(store map[K]V) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if checkFn != nil {
		if err := checkFn(s.store); err != nil {
			return err
		}
	}

	e := entry[K, V]{Op: "DELETE", Key: key}
	data, err := json.Marshal(e)
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

	delete(s.store, key)
	return nil
}

func (s *Store[K, V]) snapshotLoop() {
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

func (s *Store[K, V]) doSnapshot() {
	s.mu.Lock()
	// Copy the map to safely marshal it outside the lock
	storeCopy := make(map[K]V, len(s.store))
	maps.Copy(storeCopy, s.store)

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
