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

func TestParseNodeID_Invalid(t *testing.T) {
	_, err := ParseNodeID("invalid-hex")
	if err == nil {
		t.Error("Expected error parsing invalid hex, got nil")
	}

	_, err = NewMemoryFinder("invalid-hex")
	if err == nil {
		t.Error("Expected error creating MemoryFinder with invalid ID, got nil")
	}
}

func TestRoutingTable_AddSelfAndDupAndEviction(t *testing.T) {
	self := parse("0000000000000000000000000000000000000000000000000000000000000000")
	rt := NewRoutingTable(self)

	// 1. Add self (should be ignored)
	rt.Add(self)
	if len(rt.Snapshot()) != 0 {
		t.Error("Adding self should not add to routing table")
	}

	// 2. Add node
	n1 := parse("0000000000000000000000000000000000000000000000000000000000000001")
	rt.Add(n1)
	if len(rt.Snapshot()) != 1 {
		t.Fatal("Failed to add node")
	}

	// 3. Add same node again (moves to tail/no-op since size is 1)
	rt.Add(n1)
	if len(rt.Snapshot()) != 1 {
		t.Fatal("Routing table size changed on duplicate add")
	}

	// 4. MemoryFinder.RoutingTable method
	mf, err := NewMemoryFinder("0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if mf.RoutingTable() == nil {
		t.Error("RoutingTable() returned nil")
	}
}

func TestRoutingTable_BucketLRUAndDuplicates(t *testing.T) {
	self := parse("0000000000000000000000000000000000000000000000000000000000000000")
	rt := NewRoutingTable(self)

	// We'll add nodes that belong to the same bucket.
	// PrefixLen(node) determines the bucket.
	// Since self is all 0s, the bucket is the index of the first non-zero bit.
	// Let's use nodes that have the first non-zero bit in the first byte at bit position 7 (value 1).
	// So prefix len is 7. BucketIdx = 7.
	// We can generate up to BucketSize + 2 nodes that fall into this bucket.
	nodes := make([]NodeID, BucketSize+2)
	for i := range nodes {
		var n NodeID
		n[0] = 1
		n[1] = byte(i)
		nodes[i] = n
	}

	// Add first BucketSize nodes
	for i := 0; i < BucketSize; i++ {
		rt.Add(nodes[i])
	}

	// Verify they are all in the bucket (index 7)
	bucket7 := rt.buckets[7]
	if len(bucket7) != BucketSize {
		t.Fatalf("Expected bucket size %d, got %d", BucketSize, len(bucket7))
	}

	// Add the first node again. It should be moved to the tail of the bucket.
	firstNode := nodes[0]
	rt.Add(firstNode)
	bucket7 = rt.buckets[7]
	if len(bucket7) != BucketSize {
		t.Fatalf("Bucket size changed: %d", len(bucket7))
	}
	if !bucket7[BucketSize-1].Equals(firstNode) {
		t.Errorf("Expected first node to move to tail, but tail is %v", bucket7[BucketSize-1])
	}

	// Add a new node (nodes[BucketSize]), which should trigger eviction of the head (nodes[1])
	headNode := bucket7[0] // this is nodes[1] since nodes[0] was moved to tail
	if !headNode.Equals(nodes[1]) {
		t.Errorf("Expected nodes[1] at the head, got %v", headNode)
	}

	newNode := nodes[BucketSize]
	rt.Add(newNode)

	bucket7 = rt.buckets[7]
	// nodes[1] should be evicted, and newNode should be at the tail
	for _, n := range bucket7 {
		if n.Equals(headNode) {
			t.Error("Expected head node nodes[1] to be evicted, but it is still in the bucket")
		}
	}
	if !bucket7[BucketSize-1].Equals(newNode) {
		t.Error("Expected new node to be at the tail of the bucket")
	}
}
