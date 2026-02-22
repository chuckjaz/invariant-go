package storage

import (
	"bytes"
	"testing"
)

func TestInMemoryStorageList(t *testing.T) {
	mem := NewInMemoryStorage()

	// Initially empty
	var list []string
	for chunk := range mem.List(10) {
		list = append(list, chunk...)
	}
	if len(list) != 0 {
		t.Fatalf("Expected empty list, got %d items", len(list))
	}

	addr1, err := mem.Store(bytes.NewReader([]byte("data1")))
	if err != nil {
		t.Fatalf("Store error: %v", err)
	}

	addr2, err := mem.Store(bytes.NewReader([]byte("data2")))
	if err != nil {
		t.Fatalf("Store error: %v", err)
	}

	for chunk := range mem.List(10) {
		list = append(list, chunk...)
	}
	if len(list) != 2 {
		t.Fatalf("Expected list of size 2, got %d", len(list))
	}

	found1, found2 := false, false
	for _, a := range list {
		if a == addr1 {
			found1 = true
		} else if a == addr2 {
			found2 = true
		}
	}

	if !found1 || !found2 {
		t.Fatalf("List missing expected addresses: %v", list)
	}
}
