package kv

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"invariant/internal/content"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func TestBTree_CompareKeys(t *testing.T) {
	tests := []struct {
		a, b     BTreeKey
		expected int
	}{
		{BTreeKey{"a", 10}, BTreeKey{"b", 10}, -1},
		{BTreeKey{"b", 10}, BTreeKey{"a", 10}, 1},
		{BTreeKey{"a", 10}, BTreeKey{"a", 5}, -1},
		{BTreeKey{"a", 5}, BTreeKey{"a", 10}, 1},
		{BTreeKey{"a", 10}, BTreeKey{"a", 10}, 0},
	}

	for i, tc := range tests {
		res := CompareBTreeKey(tc.a, tc.b)
		if res != tc.expected {
			t.Errorf("Test case %d: expected %d, got %d", i, tc.expected, res)
		}
	}
}

func TestBTree_DeserializeError(t *testing.T) {
	_, err := DeserializeBTreeNode([]byte("invalid json{"))
	if err == nil {
		t.Errorf("Expected error for invalid JSON, got nil")
	}
}

func TestBTree_NilRoot(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")
	btree := NewBTree(store, slotClient, 2, content.WriterOptions{}) // maxKeys < 3 -> defaults to 3

	// Search nil root
	_, _, found, err := btree.Search(ctx, nil, "any")
	if err != nil || found {
		t.Errorf("Expected not found, got found: %t, err: %v", found, err)
	}

	// Search history nil root
	hist, hasMore, err := btree.SearchHistory(ctx, nil, "any", 0, 100, 10)
	if err != nil || len(hist) > 0 || hasMore {
		t.Errorf("Expected empty history, got values: %v, hasMore: %t, err: %v", hist, hasMore, err)
	}
}

func TestBTree_CacheHits(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")
	btree := NewBTree(store, slotClient, 3, content.WriterOptions{})

	// Construct a leaf node and save it
	node := &BTreeNode{
		IsLeaf: true,
		Keys:   []BTreeKey{{"key1", 1}},
		Values: []ValueEntry{{Inline: []byte("val1")}},
	}
	link, err := btree.saveNode(ctx, node)
	if err != nil {
		t.Fatalf("Failed to save node: %v", err)
	}

	// First load: reads from store, populates cache
	loaded, err := btree.loadNode(ctx, link)
	if err != nil {
		t.Fatalf("Failed to load node: %v", err)
	}
	if string(loaded.Values[0].Inline) != "val1" {
		t.Errorf("Expected 'val1', got '%s'", loaded.Values[0].Inline)
	}

	// Remove from storage directly
	_, err = store.Remove(ctx, link.Address)
	if err != nil {
		t.Fatalf("Failed to remove block from store: %v", err)
	}

	// Second load: should hit cache and still succeed!
	loadedCached, err := btree.loadNode(ctx, link)
	if err != nil {
		t.Fatalf("Expected cached load to succeed, but failed: %v", err)
	}
	if string(loadedCached.Values[0].Inline) != "val1" {
		t.Errorf("Expected 'val1' from cache, got '%s'", loadedCached.Values[0].Inline)
	}
}

func TestBTree_OutofLineValues(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")
	btree := NewBTree(store, slotClient, 3, content.WriterOptions{})

	// Construct a record with value exceeding ValueThreshold (1024 bytes)
	largeVal := bytes.Repeat([]byte("A"), 1500)
	recs := []Record{
		{Key: "large-key", TransactionID: 10, Value: largeVal},
	}

	root, err := btree.InsertBatch(ctx, nil, recs, nil, 10)
	if err != nil {
		t.Fatalf("Failed to insert large value: %v", err)
	}

	// Search large value
	valEntry, txID, found, err := btree.Search(ctx, &root, "large-key")
	if err != nil || !found {
		t.Fatalf("Failed to search large key: found=%t, err=%v", found, err)
	}
	if txID != 10 {
		t.Errorf("Expected txID 10, got %d", txID)
	}
	if valEntry.Link == nil {
		t.Errorf("Expected out-of-line value link, but got nil")
	}

	// SearchHistory to verify reading out-of-line value
	hist, hasMore, err := btree.SearchHistory(ctx, &root, "large-key", 0, 100, 10)
	if err != nil {
		t.Fatalf("SearchHistory failed: %v", err)
	}
	if len(hist) != 1 || hasMore {
		t.Errorf("Expected 1 hist entry, hasMore=false. got len=%d, hasMore=%t", len(hist), hasMore)
	}
	if !bytes.Equal(hist[0].Value, largeVal) {
		t.Errorf("Retrieved history value does not match original large value")
	}
}

func TestBTree_SplitsAndTraversals(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")
	// Use small maxKeys = 3 to trigger splits quickly
	btree := NewBTree(store, slotClient, 3, content.WriterOptions{})

	var root *content.ContentLink
	// Insert 20 records sequentially to build an internal node hierarchy and cause splits
	for i := range 20 {
		key := fmt.Sprintf("k-%02d", i)
		recs := []Record{
			{Key: key, TransactionID: uint64(100 + i), Value: []byte(fmt.Sprintf("v-%02d", i))},
		}
		newRoot, err := btree.InsertBatch(ctx, root, recs, nil, uint64(100+i))
		if err != nil {
			t.Fatalf("Insert failed at %d: %v", i, err)
		}
		root = &newRoot
	}

	// Verify all records exist and require internal node traversal
	for i := range 20 {
		key := fmt.Sprintf("k-%02d", i)
		valEntry, txID, found, err := btree.Search(ctx, root, key)
		if err != nil || !found {
			t.Fatalf("Search failed for %s: found=%t, err=%v", key, found, err)
		}
		if txID != uint64(100+i) {
			t.Errorf("Expected txID %d, got %d", 100+i, txID)
		}
		if string(valEntry.Inline) != fmt.Sprintf("v-%02d", i) {
			t.Errorf("Expected v-%02d, got %s", i, string(valEntry.Inline))
		}
	}

	// Insert a duplicate key with newer transaction ID to test in-place leaf replacement
	recs := []Record{
		{Key: "k-05", TransactionID: 205, Value: []byte("v-05-new")},
	}
	newRoot, err := btree.InsertBatch(ctx, root, recs, nil, 205)
	if err != nil {
		t.Fatalf("Failed duplicate insert: %v", err)
	}
	root = &newRoot

	// Verify latest version is retrieved
	_, txID, found, err := btree.Search(ctx, root, "k-05")
	if err != nil || !found || txID != 205 {
		t.Errorf("Expected new version 205, found=%t, txID=%d, err=%v", found, txID, err)
	}

	// Retrieve history with limit = 1 to test Page Limit
	hist, hasMore, err := btree.SearchHistory(ctx, root, "k-05", 0, 300, 1)
	if err != nil {
		t.Fatalf("SearchHistory failed: %v", err)
	}
	if len(hist) != 1 || !hasMore {
		t.Errorf("Expected history of 1, hasMore=true. Got len=%d, hasMore=%t", len(hist), hasMore)
	}
	if string(hist[0].Value) != "v-05-new" {
		t.Errorf("Expected 'v-05-new', got '%s'", string(hist[0].Value))
	}
}

func TestBTree_NodeReadError(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot")
	btree := NewBTree(store, slotClient, 3, content.WriterOptions{})

	badLink := content.ContentLink{Address: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}

	// Search on non-existent root link should fail
	_, _, _, err := btree.Search(ctx, &badLink, "anykey")
	if err == nil {
		t.Errorf("Expected error for non-existent root address, got nil")
	}

	// SearchHistory on non-existent root link should fail
	_, _, err = btree.SearchHistory(ctx, &badLink, "anykey", 0, 100, 10)
	if err == nil {
		t.Errorf("Expected history search error for non-existent root, got nil")
	}

	// InsertBatch on non-existent root link should fail
	_, err = btree.InsertBatch(ctx, &badLink, []Record{{Key: "k", Value: []byte("v")}}, nil, 1)
	if err == nil {
		t.Errorf("Expected InsertBatch error for non-existent root, got nil")
	}
}
