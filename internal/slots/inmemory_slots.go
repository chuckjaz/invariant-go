// Package slots provides the in-memory implementation for the slots service.
package slots

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"sync"
)

// MemorySlots provides an in-memory implementation of the Slots interface.
type MemorySlots struct {
	id          string
	mu          sync.RWMutex
	slots       map[string]SlotRecord
	subscribers []chan string
}

// NewMemorySlots creates a new MemorySlots instance.
func NewMemorySlots(id string) *MemorySlots {
	return &MemorySlots{
		id:    id,
		slots: make(map[string]SlotRecord),
	}
}

// ID returns the service ID.
func (m *MemorySlots) ID() string {
	return m.id
}

// Get returns the address for the given slot ID.
func (m *MemorySlots) Get(ctx context.Context, id string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	record, ok := m.slots[id]
	if !ok {
		return "", ErrSlotNotFound
	}

	return record.Address, nil
}

// Update attempts to change the address of a slot, ensuring the previous address matches.
// If the slot policy is "ecc", it will verify the request using the passed ed25519 auth signature.
func (m *MemorySlots) Update(ctx context.Context, id string, address string, previousAddress string, auth []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, ok := m.slots[id]
	if !ok {
		return ErrSlotNotFound
	}

	if record.Policy == "ecc" {
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

	if record.Address != previousAddress {
		return ErrConflict
	}

	record.Address = address
	m.slots[id] = record
	return nil
}

// Create creates a new slot with the given address and policy.
func (m *MemorySlots) Create(ctx context.Context, id string, address string, policy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.slots[id]; exists {
		return ErrSlotExists
	}

	m.slots[id] = SlotRecord{Address: address, Policy: policy}
	m.notifySubscribers(id)
	return nil
}

// List returns a channel that yields chunks of all known slot IDs.
func (m *MemorySlots) List(ctx context.Context, chunkSize int) <-chan []string {
	if chunkSize <= 0 {
		chunkSize = 10000
	}
	ch := make(chan []string)

	go func() {
		defer close(ch)
		m.mu.RLock()
		defer m.mu.RUnlock()

		var chunk []string
		for id := range m.slots {
			chunk = append(chunk, id)
			if len(chunk) >= chunkSize {
				ch <- chunk
				chunk = nil
			}
		}
		if len(chunk) > 0 {
			ch <- chunk
		}
	}()

	return ch
}

// Subscribe returns a channel that yields the IDs of newly created slots.
func (m *MemorySlots) Subscribe(ctx context.Context) <-chan string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan string, 100)
	m.subscribers = append(m.subscribers, ch)
	return ch
}

func (m *MemorySlots) notifySubscribers(id string) {
	// Note: We don't take the full mutex lock during notification to avoid deadlocks
	// if a subscriber's channel blocks. We expect the caller to hold m.mu.Lock().
	for _, ch := range m.subscribers {
		select {
		case ch <- id:
		default:
			// Subscriber is full or blocked, drop the notification
		}
	}
}
