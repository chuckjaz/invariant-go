package distribute

import (
	"invariant/internal/container"
	"math/bits"
)

// Distribute defines the core logic for managing the distribution of blobs.
type Distribute interface {
	container.Container
	Register(id string) error
}

// Distance calculates the Kademlia distance between two 32-byte IDs represented as byte slices.
// The distance is the XOR of the two IDs. A shorter distance means the IDs are closer.
// To use this for comparing distances, one can simply use bytes.Compare on the results,
// or count the number of leading zero bits.
func Distance(a, b []byte) []byte {
	if len(a) != len(b) {
		return nil // Should be 32 bytes
	}
	dist := make([]byte, len(a))
	for i := range a {
		dist[i] = a[i] ^ b[i]
	}
	return dist
}

// CmpDistance compares two distances. It returns -1 if d1 < d2, 0 if d1 == d2, and 1 if d1 > d2.
// The distances are byte slices, typically representing 256-bit numbers (32 bytes).
func CmpDistance(d1, d2 []byte) int {
	if len(d1) != len(d2) {
		return 0
	}
	for i := range d1 {
		if d1[i] < d2[i] {
			return -1
		} else if d1[i] > d2[i] {
			return 1
		}
	}
	return 0
}

// PrefixLen returns the length of the common prefix in bits (the number of leading zero bits in the distance).
func PrefixLen(distance []byte) int {
	for i, b := range distance {
		if b != 0 {
			return i*8 + bits.LeadingZeros8(uint8(b))
		}
	}
	return len(distance) * 8
}
