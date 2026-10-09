package chainedjournal

import (
	"context"
	"fmt"
	"testing"
	"time"

	"invariant/internal/slots"
	"invariant/internal/storage"
)

type testItem struct {
	ID    int    `json:"id"`
	Value string `json:"val"`
}

func TestChainedJournal_LocalAndAutoFlush(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotClient := slots.NewMemorySlots("test-slot-service")
	tempDir := t.TempDir()

	j, err := NewChainedJournal[testItem](ChainedJournalOptions{
		BaseDir:    tempDir,
		Storage:    store,
		Slots:      slotClient,
		SlotID:     "slot-1",
		MaxEntries: 2,
	})
	if err != nil {
		t.Fatalf("Failed to create ChainedJournal: %v", err)
	}
	defer j.Close()

	// Append item 1
	flushed, err := j.Append(ctx, testItem{ID: 1, Value: "alpha"}, false)
	if err != nil {
		t.Fatalf("Append 1 failed: %v", err)
	}
	if flushed {
		t.Errorf("Expected flushed=false on item 1")
	}

	// Append item 2 -> triggers auto-flush
	flushed, err = j.Append(ctx, testItem{ID: 2, Value: "beta"}, false)
	if err != nil {
		t.Fatalf("Append 2 failed: %v", err)
	}
	if !flushed {
		t.Errorf("Expected flushed=true on item 2")
	}

	// Wait for slot to be populated
	var slotVal string
	for range 50 {
		slotVal, err = slotClient.Get(ctx, "slot-1")
		if err == nil && slotVal != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if slotVal == "" {
		t.Fatalf("Slot was not updated with journal link")
	}

	// Append item 3 and 4 -> triggers second flush and links back to page 1
	_, err = j.Append(ctx, testItem{ID: 3, Value: "gamma"}, false)
	if err != nil {
		t.Fatalf("Append 3 failed: %v", err)
	}
	_, err = j.Append(ctx, testItem{ID: 4, Value: "delta"}, false)
	if err != nil {
		t.Fatalf("Append 4 failed: %v", err)
	}

	// Flush to ensure background upload finishes
	if err := j.Flush(ctx); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Load remote entries and check order
	records, err := j.LoadRemoteEntries(ctx, nil)
	if err != nil {
		t.Fatalf("LoadRemoteEntries failed: %v", err)
	}

	if len(records) != 4 {
		t.Fatalf("Expected 4 records, got %d: %+v", len(records), records)
	}

	for i, expected := range []string{"alpha", "beta", "gamma", "delta"} {
		if records[i].Value != expected {
			t.Errorf("Record %d: expected %s, got %s", i, expected, records[i].Value)
		}
	}
}

func TestChainedJournal_LocalEntriesOnly(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	// No storage or slots provided -> pure local journal
	j, err := NewChainedJournal[testItem](ChainedJournalOptions{
		BaseDir:    tempDir,
		MaxEntries: 100,
	})
	if err != nil {
		t.Fatalf("Failed to create ChainedJournal: %v", err)
	}
	defer j.Close()

	for i := 1; i <= 5; i++ {
		_, err := j.Append(ctx, testItem{ID: i, Value: fmt.Sprintf("item-%d", i)}, true)
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	local, err := j.LoadLocalEntries()
	if err != nil {
		t.Fatalf("LoadLocalEntries failed: %v", err)
	}
	if len(local) != 5 {
		t.Fatalf("Expected 5 local entries, got %d", len(local))
	}
	if local[0].Value != "item-1" || local[4].Value != "item-5" {
		t.Errorf("Unexpected local entries: %+v", local)
	}
}
