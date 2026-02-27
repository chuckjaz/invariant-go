// Package slots provides the core interface for the slots service.
package slots

import "errors"

// ErrSlotNotFound is returned when a slot doesn't exist.
var ErrSlotNotFound = errors.New("slot not found")

// ErrConflict is returned when attempting to update a slot with an incorrect previousAddress.
var ErrConflict = errors.New("conflict: previous address does not match")

// ErrSlotExists is returned when attempting to create a slot that already exists.
var ErrSlotExists = errors.New("slot already exists")

// SlotUpdate represents a request to update a slot's address.
type SlotUpdate struct {
	Address         string `json:"address"`
	PreviousAddress string `json:"previousAddress"`
}

// SlotRegistration represents a request to create a new slot.
type SlotRegistration struct {
	Address string `json:"address"`
}

// Slots defines the interface for a slots service.
// It maps 32-byte hex encoded IDs to strings representing the sha256 hash of a content block.
type Slots interface {
	// ID returns the ID of the slots service itself.
	ID() string

	// Get returns the address for the given slot ID.
	Get(id string) (string, error)

	// Update sets the new address for a generic id, expecting previousAddress to match the current value.
	Update(id string, address string, previousAddress string) error

	// Create creates a new slot with the given id and address.
	Create(id string, address string) error
}
