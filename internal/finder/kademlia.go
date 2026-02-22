package finder

import (
	"bytes"
	"encoding/hex"
	"sort"
)

const (
	// Kademlia K value (bucket size)
	BucketSize = 20
	// Length of Node ID in bytes
	IDLength = 32
)

// NodeID represents a Kademlia node ID.
type NodeID [IDLength]byte

// ParseNodeID parses a hex-encoded string into a NodeID.
func ParseNodeID(idHex string) (NodeID, error) {
	var id NodeID
	b, err := hex.DecodeString(idHex)
	if err != nil {
		return id, err
	}
	copy(id[:], b)
	return id, nil
}

// String returns the hex-encoded string representation of a NodeID.
func (n NodeID) String() string {
	return hex.EncodeToString(n[:])
}

// Equals returns true if two NodeIDs are equal.
func (n NodeID) Equals(other NodeID) bool {
	return bytes.Equal(n[:], other[:])
}

// XOR computes the distance between two NodeIDs.
func (n NodeID) XOR(other NodeID) NodeID {
	var distance NodeID
	for i := 0; i < IDLength; i++ {
		distance[i] = n[i] ^ other[i]
	}
	return distance
}

// Less answers if the distance of node n to target is less than node other to target.
func (n NodeID) Less(other, target NodeID) bool {
	nDist := n.XOR(target)
	oDist := other.XOR(target)
	return bytes.Compare(nDist[:], oDist[:]) < 0
}

// PrefixLen returns the number of common bits between two NodeIDs.
func (n NodeID) PrefixLen(other NodeID) int {
	for i := 0; i < IDLength; i++ {
		b := n[i] ^ other[i]
		if b != 0 {
			// Find the first set bit from the left
			for j := 0; j < 8; j++ {
				if (b & (1 << (7 - j))) != 0 {
					return i*8 + j
				}
			}
		}
	}
	return IDLength * 8
}

// RoutingTable manages Kademlia K-Buckets.
type RoutingTable struct {
	self    NodeID
	buckets [IDLength * 8][]NodeID
}

// NewRoutingTable creates a new RoutingTable.
func NewRoutingTable(self NodeID) *RoutingTable {
	return &RoutingTable{
		self: self,
	}
}

// Add inserts or updates a node in the routing table.
func (rt *RoutingTable) Add(node NodeID) {
	if node.Equals(rt.self) {
		return
	}
	bucketIdx := rt.self.PrefixLen(node)
	if bucketIdx == IDLength*8 {
		return // Should not happen if it's not self
	}

	bucket := rt.buckets[bucketIdx]

	// Check if already in bucket
	for i, n := range bucket {
		if n.Equals(node) {
			// Move to tail (most recently seen)
			rt.buckets[bucketIdx] = append(append(bucket[:i], bucket[i+1:]...), node)
			return
		}
	}

	if len(bucket) < BucketSize {
		rt.buckets[bucketIdx] = append(bucket, node)
	} else {
		// In a real Kademlia, we would ping the head of the bucket and only evict if it's dead.
		// For now, we'll just drop the oldest (head) and insert the new one if we wanted a simple LRU.
		// A strictly conforming Wikipedia algorithm does the ping. Since we only have memory,
		// and simple nodes, we'll just not add it for simplicity, to match standard basic docs unless
		// instructed otherwise. Wait, keeping fresh nodes is better if we assume all are alive.
		// Let's implement simple LRU for dead-node resistance: drop head, add to tail.
		rt.buckets[bucketIdx] = append(bucket[1:], node)
	}
}

// FindClosest returns the up to `count` closest nodes to the target in the routing table.
func (rt *RoutingTable) FindClosest(target NodeID, count int) []NodeID {
	var allNodes []NodeID
	for _, bucket := range rt.buckets {
		allNodes = append(allNodes, bucket...)
	}

	sort.Slice(allNodes, func(i, j int) bool {
		return allNodes[i].Less(allNodes[j], target)
	})

	if len(allNodes) > count {
		return allNodes[:count]
	}
	return allNodes
}

// Snapshot returns all nodes in the routing table.
func (rt *RoutingTable) Snapshot() []NodeID {
	var allNodes []NodeID
	for _, bucket := range rt.buckets {
		allNodes = append(allNodes, bucket...)
	}
	return allNodes
}
