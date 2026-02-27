package finder

import (
	"crypto/rand"
	"testing"
)

func randomNodeID() NodeID {
	var id NodeID
	rand.Read(id[:])
	return id
}

func parse(hex string) NodeID {
	id, _ := ParseNodeID(hex)
	return id
}

func TestXORDistance(t *testing.T) {
	n1 := parse("0000000000000000000000000000000000000000000000000000000000000001")
	n2 := parse("0000000000000000000000000000000000000000000000000000000000000002")

	dist := n1.XOR(n2)
	expected := parse("0000000000000000000000000000000000000000000000000000000000000003")
	if !dist.Equals(expected) {
		t.Errorf("Expected XOR %s, got %s", expected, dist)
	}
}

func TestPrefixLen(t *testing.T) {
	n1 := parse("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	n2 := parse("fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffe")
	n3 := parse("7fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	// Same should be full length
	if n1.PrefixLen(n1) != IDLength*8 {
		t.Errorf("Expected 256 for identical IDs")
	}

	// Difference in last byte should be 255 common bits
	if n1.PrefixLen(n2) != 255 {
		t.Errorf("Expected 255 common bits, got %d", n1.PrefixLen(n2))
	}

	// Difference in first bit should be 0 common bits
	if n1.PrefixLen(n3) != 0 {
		t.Errorf("Expected 0 common bits, got %d", n1.PrefixLen(n3))
	}
}

func TestRoutingTableCapacity(t *testing.T) {
	self := randomNodeID()
	rt := NewRoutingTable(self)

	// Add 30 random nodes. Because they are random, they will almost certainly
	// fall into the 0-prefix bucket.
	for range 30 {
		// Flip the first bit so it's guaranteed to be distance 255 (prefix len 0)
		var other NodeID
		rand.Read(other[:])
		other[0] = self[0] ^ 0x80
		rt.Add(other)
	}

	// Bucket size is 20, so only 20 should make it in.
	nodes := rt.Snapshot()
	if len(nodes) != BucketSize {
		t.Errorf("Expected routing table to keep %d nodes, but has %d", BucketSize, len(nodes))
	}
}

func TestFindClosest(t *testing.T) {
	self := parse("0000000000000000000000000000000000000000000000000000000000000000")
	rt := NewRoutingTable(self)

	// Add nodes at varying distances
	n1 := parse("0000000000000000000000000000000000000000000000000000000000000001") // Dist 1
	n2 := parse("0000000000000000000000000000000000000000000000000000000000000002") // Dist 2
	n3 := parse("0000000000000000000000000000000000000000000000000000000000000004") // Dist 4
	n4 := parse("0000000000000000000000000000000000000000000000000000000000000008") // Dist 8

	rt.Add(n1)
	rt.Add(n4)
	rt.Add(n2)
	rt.Add(n3)

	// Target is n1. We expect n1, n2, n3 to be returned in order of closeness to n1.
	target := n1

	// n1 ^ target = 0
	// n2 ^ target (2^1 = 3)
	// n3 ^ target (4^1 = 5)
	// n4 ^ target (8^1 = 9)

	closest := rt.FindClosest(target, 3)
	if len(closest) != 3 {
		t.Fatalf("Expected 3 nodes")
	}

	if !closest[0].Equals(n1) {
		t.Errorf("Expected n1 to be closest")
	}
	if !closest[1].Equals(n2) {
		t.Errorf("Expected n2 to be 2nd closest")
	}
	if !closest[2].Equals(n3) {
		t.Errorf("Expected n3 to be 3rd closest")
	}
}
