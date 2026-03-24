package discovery

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Assert that FileSystemDiscovery implements the Discovery interface
var _ Discovery = (*FileSystemDiscovery)(nil)

type FileSystemDiscovery struct {
	mu               sync.RWMutex
	store            map[string]ServiceRegistration
	baseDir          string
	journalFile      *os.File
	journalName      string
	snapshotInterval time.Duration
	stopCh           chan struct{}
}

type journalEntry struct {
	Op      string              `json:"op"` // "REGISTER"
	ID      string              `json:"id"`
	Service ServiceRegistration `json:"service,omitempty"`
}

func NewFileSystemDiscovery(baseDir string, snapshotInterval time.Duration) (*FileSystemDiscovery, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, err
	}

	fsd := &FileSystemDiscovery{
		store:            make(map[string]ServiceRegistration),
		baseDir:          baseDir,
		snapshotInterval: snapshotInterval,
		stopCh:           make(chan struct{}),
	}

	// 1. Load snapshot
	snapshotPath := filepath.Join(baseDir, "snapshot.json")
	if data, err := os.ReadFile(snapshotPath); err == nil {
		if err := json.Unmarshal(data, &fsd.store); err != nil {
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
		if err := fsd.applyJournal(filepath.Join(baseDir, journal)); err != nil {
			return nil, fmt.Errorf("failed to apply journal %s: %w", journal, err)
		}
	}

	// 3. Open new journal
	if err := fsd.openNewJournal(); err != nil {
		return nil, err
	}

	// 4. Start background snapshot goroutine if interval > 0
	if snapshotInterval > 0 {
		go fsd.snapshotLoop()
	}

	return fsd, nil
}

func (d *FileSystemDiscovery) applyJournal(path string) error {
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
		case "REGISTER":
			d.store[entry.ID] = entry.Service
		}
	}
	return scanner.Err()
}

func (d *FileSystemDiscovery) openNewJournal() error {
	name := fmt.Sprintf("journal-%d.jsonl", time.Now().UnixNano())
	path := filepath.Join(d.baseDir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	if d.journalFile != nil {
		d.journalFile.Close()
	}

	d.journalFile = file
	d.journalName = name
	return nil
}

func (d *FileSystemDiscovery) Close() error {
	close(d.stopCh)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.journalFile != nil {
		err := d.journalFile.Close()
		d.journalFile = nil
		return err
	}
	return nil
}

func (d *FileSystemDiscovery) Get(ctx context.Context, id string) (ServiceDescription, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	reg, ok := d.store[id]
	if !ok {
		return ServiceDescription{}, false
	}

	protocolsCopy := make([]string, len(reg.Protocols))
	copy(protocolsCopy, reg.Protocols)

	return ServiceDescription{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: protocolsCopy,
	}, true
}

func (d *FileSystemDiscovery) Find(ctx context.Context, protocol string, count int) ([]ServiceDescription, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var results []ServiceDescription
	for _, reg := range d.store {
		if protocol == "" {
			continue
		}

		if slices.Contains(reg.Protocols, protocol) {
			protocolsCopy := make([]string, len(reg.Protocols))
			copy(protocolsCopy, reg.Protocols)
			results = append(results, ServiceDescription{
				ID:        reg.ID,
				Address:   reg.Address,
				Protocols: protocolsCopy,
			})
			if len(results) >= count {
				break
			}
		}
	}

	return results, nil
}

func (d *FileSystemDiscovery) Register(ctx context.Context, reg ServiceRegistration) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	protocolsCopy := make([]string, len(reg.Protocols))
	copy(protocolsCopy, reg.Protocols)
	regCopy := ServiceRegistration{
		ID:        reg.ID,
		Address:   reg.Address,
		Protocols: protocolsCopy,
	}

	entry := journalEntry{
		Op:      "REGISTER",
		ID:      reg.ID,
		Service: regCopy,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if _, err := d.journalFile.Write(data); err != nil {
		return err
	}
	if err := d.journalFile.Sync(); err != nil {
		return err
	}

	d.store[reg.ID] = regCopy
	return nil
}

func (d *FileSystemDiscovery) snapshotLoop() {
	ticker := time.NewTicker(d.snapshotInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.stopCh:
			return
		case <-ticker.C:
			d.doSnapshot()
		}
	}
}

func (d *FileSystemDiscovery) doSnapshot() {
	d.mu.Lock()
	// Copy the map to safely marshal it outside the lock
	storeCopy := make(map[string]ServiceRegistration, len(d.store))
	for k, v := range d.store {
		protocolsCopy := make([]string, len(v.Protocols))
		copy(protocolsCopy, v.Protocols)
		storeCopy[k] = ServiceRegistration{
			ID:        v.ID,
			Address:   v.Address,
			Protocols: protocolsCopy,
		}
	}

	// Create new journal while holding the lock
	if err := d.openNewJournal(); err != nil {
		d.mu.Unlock()
		return
	}
	newJournal := d.journalName
	d.mu.Unlock()

	// 1. Write the copied store to a temporary snapshot file
	tmpPath := filepath.Join(d.baseDir, "snapshot.tmp")
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
	finalPath := filepath.Join(d.baseDir, "snapshot.json")
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return
	}

	// 3. Safely remove old journals
	entries, err := os.ReadDir(d.baseDir)
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasPrefix(entry.Name(), "journal-") && strings.HasSuffix(entry.Name(), ".jsonl") {
				if entry.Name() != newJournal {
					os.Remove(filepath.Join(d.baseDir, entry.Name()))
				}
			}
		}
	}
}
