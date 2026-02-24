package names_test

import (
	"invariant/internal/names"
	"testing"
)

func TestInMemoryNames_PutAndGet(t *testing.T) {
	store := names.NewInMemoryNames()

	err := store.Put("my-name", "12345", []string{"names-v1", "storage-v1"})
	if err != nil {
		t.Fatalf("unexpected error on Put: %v", err)
	}

	entry, err := store.Get("my-name")
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

	_, err := store.Get("non-existent")
	if err != names.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestInMemoryNames_DeleteSuccess(t *testing.T) {
	store := names.NewInMemoryNames()
	store.Put("to-delete", "abc", []string{"test-v1"})

	err := store.Delete("to-delete", "abc")
	if err != nil {
		t.Fatalf("unexpected error on Delete: %v", err)
	}

	_, err = store.Get("to-delete")
	if err != names.ErrNotFound {
		t.Errorf("expected ErrNotFound after deletion, got %v", err)
	}
}

func TestInMemoryNames_DeletePreconditionFailed(t *testing.T) {
	store := names.NewInMemoryNames()
	store.Put("to-delete", "abc", []string{"test-v1"})

	err := store.Delete("to-delete", "def")
	if err != names.ErrPreconditionFailed {
		t.Errorf("expected ErrPreconditionFailed, got %v", err)
	}

	// Should still exist
	entry, err := store.Get("to-delete")
	if err != nil {
		t.Fatalf("unexpected error on Get: %v", err)
	}
	if entry.Value != "abc" {
		t.Errorf("expected value 'abc', got '%s'", entry.Value)
	}
}

func TestInMemoryNames_DeleteWithoutETag(t *testing.T) {
	store := names.NewInMemoryNames()
	store.Put("to-delete", "abc", []string{"test-v1"})

	err := store.Delete("to-delete", "")
	if err != nil {
		t.Fatalf("unexpected error on Delete: %v", err)
	}

	_, err = store.Get("to-delete")
	if err != names.ErrNotFound {
		t.Errorf("expected ErrNotFound after deletion, got %v", err)
	}
}

func TestInMemoryNames_DataRaceAndTokensCopy(t *testing.T) {
	store := names.NewInMemoryNames()
	tokens := []string{"a", "b"}
	store.Put("name", "val", tokens)

	// Modify original tokens
	tokens[0] = "c"

	entry, _ := store.Get("name")
	if entry.Tokens[0] != "a" {
		t.Errorf("Tokens array in store was mutated! Expected 'a', got '%s'", entry.Tokens[0])
	}

	// Modify returned tokens
	entry.Tokens[0] = "d"

	entry2, _ := store.Get("name")
	if entry2.Tokens[0] != "a" {
		t.Errorf("Tokens array in store was mutated by modifying Get result! Expected 'a', got '%s'", entry2.Tokens[0])
	}
}
