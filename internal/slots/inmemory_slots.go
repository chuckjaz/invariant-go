// Package slots provides the in-memory implementation for the slots service.
package slots

import (
	"sync"
)

// MemorySlots provides an in-memory implementation of the Slots interface.
type MemorySlots struct {
	id          string
	mu          sync.RWMutex
	slots       map[string]string
	subscribers []chan string
}

// NewMemorySlots creates a new MemorySlots instance.
func NewMemorySlots(id string) *MemorySlots {
	return &MemorySlots{
		id:    id,
		slots: make(map[string]string),
	}
}

// ID returns the service ID.
func (m *MemorySlots) ID() string {
	return m.id
}

// Get returns the address for the given slot ID.
func (m *MemorySlots) Get(id string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	addr, ok := m.slots[id]
	if !ok {
		return "", ErrSlotNotFound
	}

	return addr, nil
}

// Update attempts to change the address of a slot, ensuring the previous address matches.
func (m *MemorySlots) Update(id string, address string, previousAddress string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	currentAddr, ok := m.slots[id]
	if !ok {
		return ErrSlotNotFound
	}
	if currentAddr != previousAddress {
		return ErrConflict
	}

	m.slots[id] = address
	return nil
}

// Create creates a new slot with the given address.
func (m *MemorySlots) Create(id string, address string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.slots[id]; exists {
		return ErrSlotExists
	}

	m.slots[id] = address
	m.notifySubscribers(id)
	return nil
}

// List returns a channel that yields chunks of all known slot IDs.
func (m *MemorySlots) List(chunkSize int) <-chan []string {
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
func (m *MemorySlots) Subscribe() <-chan string {
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
