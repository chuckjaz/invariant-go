// Package slots provides the file system implementation for the slots service.
package slots

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"invariant/internal/journal"
)

var _ Slots = (*FileSystemSlots)(nil)

// FileSystemSlots provides a file system-backed implementation of the Slots interface.
type FileSystemSlots struct {
	id          string
	subMu       sync.RWMutex
	subscribers []chan string
	store       *journal.Store[string, SlotRecord]
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

	store, err := journal.NewStore[string, SlotRecord](baseDir, snapshotInterval)
	if err != nil {
		return nil, err
	}

	return &FileSystemSlots{
		id:    id,
		store: store,
	}, nil
}

// ID returns the service ID.
func (s *FileSystemSlots) ID() string {
	return s.id
}

// Close closes the file system slots, stopping the snapshot loop and closing the journal file.
func (s *FileSystemSlots) Close() error {
	return s.store.Close()
}

// Get returns the address for the given slot ID.
func (s *FileSystemSlots) Get(ctx context.Context, id string) (string, error) {
	record, ok := s.store.Get(id)
	if !ok {
		return "", ErrSlotNotFound
	}
	return record.Address, nil
}

// Create creates a new slot with the given address and policy.
func (s *FileSystemSlots) Create(ctx context.Context, id string, address string, policy string) error {
	record := SlotRecord{Address: address, Policy: policy}

	err := s.store.Put(id, record, func(store map[string]SlotRecord) error {
		if _, exists := store[id]; exists {
			return ErrSlotExists
		}
		return nil
	})

	if err == nil {
		s.notifySubscribers(id)
	}
	return err
}

// List returns a channel that yields chunks of all known slot IDs.
func (s *FileSystemSlots) List(ctx context.Context, chunkSize int) <-chan []string {
	if chunkSize <= 0 {
		chunkSize = 10000
	}
	ch := make(chan []string)

	go func() {
		defer close(ch)
		s.store.Read(func(store map[string]SlotRecord) {
			var chunk []string
			for id := range store {
				chunk = append(chunk, id)
				if len(chunk) >= chunkSize {
					ch <- chunk
					chunk = nil
				}
			}
			if len(chunk) > 0 {
				ch <- chunk
			}
		})
	}()

	return ch
}

// Subscribe returns a channel that yields the IDs of newly created slots.
func (s *FileSystemSlots) Subscribe(ctx context.Context) <-chan string {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	ch := make(chan string, 100)
	s.subscribers = append(s.subscribers, ch)
	return ch
}

func (s *FileSystemSlots) notifySubscribers(id string) {
	s.subMu.RLock()
	defer s.subMu.RUnlock()
	for _, ch := range s.subscribers {
		select {
		case ch <- id:
		default:
			// Subscriber is full or blocked, drop the notification
		}
	}
}

// Update attempts to change the address of a slot, ensuring the previous address matches.
// If the slot policy is "ecc", it will verify the request using the passed ed25519 auth signature.
func (s *FileSystemSlots) Update(ctx context.Context, id string, address string, previousAddress string, auth []byte) error {
	// We instantiate the new record conditionally within the Put but it must evaluate synchronously wait...
	// We can't pass record down if policy depends on checkFn?
	// Ah, the Put receives the value `v`. We need `policy` which comes from existing record.
	// We can read it first with Get:
	record, ok := s.store.Get(id)
	if !ok {
		return ErrSlotNotFound
	}

	newRecord := SlotRecord{Address: address, Policy: record.Policy}

	return s.store.Put(id, newRecord, func(store map[string]SlotRecord) error {
		// Verify again under the store lock to avoid races
		existing, ok := store[id]
		if !ok {
			return ErrSlotNotFound
		}

		if existing.Policy == "ecc" {
			pubKey, err := hex.DecodeString(id)
			if err != nil || len(pubKey) != ed25519.PublicKeySize {
				return ErrUnauthorized
			}

			reqData, _ := json.Marshal(SlotUpdate{
				Address:         address,
				PreviousAddress: previousAddress,
			})

			if !ed25519.Verify(pubKey, reqData, auth) {
				return ErrUnauthorized
			}
		}

		if existing.Address != previousAddress {
			return ErrConflict
		}

		return nil
	})
}
