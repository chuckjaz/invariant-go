package names_test

import (
	"context"
	"invariant/internal/names"
	"testing"
)

func TestInMemoryNames_PutAndGet(t *testing.T) {
	store := names.NewInMemoryNames()

	err := store.Put(context.Background(), "my-name", "12345", []string{"names-v1", "storage-v1"})
	if err != nil {
		t.Fatalf("unexpected error on Put: %v", err)
	}

	entry, err := store.Get(context.Background(), "my-name")
	if err != nil {
		t.Fatalf("unexpected error on Get: %v", err)
	}

	if entry.Value != "12345" {
		t.Errorf("expected value '12345', got '%s'", entry.Value)
	}

	if len(entry.Tokens) != 2 || entry.Tokens[0] != "names-v1" || entry.Tokens[1] != "storage-v1" {
		t.Errorf("unexpected tokens: %v", entry.Tokens)
	}
}

func TestInMemoryNames_GetNotFound(t *testing.T) {
	store := names.NewInMemoryNames()

	_, err := store.Get(context.Background(), "non-existent")
	if err != names.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestInMemoryNames_DeleteSuccess(t *testing.T) {
	store := names.NewInMemoryNames()
	store.Put(context.Background(), "to-delete", "abc", []string{"test-v1"})

	err := store.Delete(context.Background(), "to-delete", "abc")
	if err != nil {
		t.Fatalf("unexpected error on Delete: %v", err)
	}

	_, err = store.Get(context.Background(), "to-delete")
	if err != names.ErrNotFound {
		t.Errorf("expected ErrNotFound after deletion, got %v", err)
	}
}

func TestInMemoryNames_DeletePreconditionFailed(t *testing.T) {
	store := names.NewInMemoryNames()
	store.Put(context.Background(), "to-delete", "abc", []string{"test-v1"})

	err := store.Delete(context.Background(), "to-delete", "def")
	if err != names.ErrPreconditionFailed {
		t.Errorf("expected ErrPreconditionFailed, got %v", err)
	}

	// Should still exist
	entry, err := store.Get(context.Background(), "to-delete")
	if err != nil {
		t.Fatalf("unexpected error on Get: %v", err)
	}
	if entry.Value != "abc" {
		t.Errorf("expected value 'abc', got '%s'", entry.Value)
	}
}

func TestInMemoryNames_DeleteWithoutETag(t *testing.T) {
	store := names.NewInMemoryNames()
	store.Put(context.Background(), "to-delete", "abc", []string{"test-v1"})

	err := store.Delete(context.Background(), "to-delete", "")
	if err != nil {
		t.Fatalf("unexpected error on Delete: %v", err)
	}

	_, err = store.Get(context.Background(), "to-delete")
	if err != names.ErrNotFound {
		t.Errorf("expected ErrNotFound after deletion, got %v", err)
	}
}

func TestInMemoryNames_DataRaceAndTokensCopy(t *testing.T) {
	store := names.NewInMemoryNames()
	tokens := []string{"a", "b"}
	store.Put(context.Background(), "name", "val", tokens)

	// Modify original tokens
	tokens[0] = "c"

	entry, _ := store.Get(context.Background(), "name")
	if entry.Tokens[0] != "a" {
		t.Errorf("Tokens array in store was mutated! Expected 'a', got '%s'", entry.Tokens[0])
	}

	// Modify returned tokens
	entry.Tokens[0] = "d"

	entry2, _ := store.Get(context.Background(), "name")
	if entry2.Tokens[0] != "a" {
		t.Errorf("Tokens array in store was mutated by modifying Get result! Expected 'a', got '%s'", entry2.Tokens[0])
	}
}

func TestInMemoryNames_IDAndLookup(t *testing.T) {
	store := names.NewInMemoryNames()
	id := store.ID()
	if len(id) != 64 {
		t.Errorf("expected 64-char ID, got %s", id)
	}

	ctx := context.Background()
	store.Put(ctx, "name1", "id-abc", []string{})
	store.Put(ctx, "name2", "id-xyz", []string{})
	store.Put(ctx, "name3", "id-abc", []string{})

	results, err := store.Lookup(ctx, "id-abc")
	if err != nil {
		t.Fatalf("unexpected lookup error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 lookup results, got %v", results)
	}
	hasName1 := false
	hasName3 := false
	for _, name := range results {
		if name == "name1" {
			hasName1 = true
		}
		if name == "name3" {
			hasName3 = true
		}
	}
	if !hasName1 || !hasName3 {
		t.Errorf("lookup results did not contain expected names: %v", results)
	}

	emptyResults, err := store.Lookup(ctx, "id-nonexistent")
	if err != nil {
		t.Fatalf("unexpected lookup error: %v", err)
	}
	if len(emptyResults) != 0 {
		t.Errorf("expected empty lookup results, got %v", emptyResults)
	}
}
