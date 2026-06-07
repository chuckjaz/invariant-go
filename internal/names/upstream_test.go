package names

import (
	"context"
	"testing"
)

// mockParentNames simulates an external network names registry.
type mockParentNames struct {
	names map[string]NameEntry
}

func newMockParent() *mockParentNames {
	return &mockParentNames{
		names: make(map[string]NameEntry),
	}
}

func (m *mockParentNames) Get(ctx context.Context, name string) (NameEntry, error) {
	entry, ok := m.names[name]
	if !ok {
		return NameEntry{}, ErrNotFound
	}
	return entry, nil
}

func (m *mockParentNames) Put(ctx context.Context, name string, value string, tokens []string) error {
	m.names[name] = NameEntry{
		Value:  value,
		Tokens: tokens,
	}
	return nil
}

func (m *mockParentNames) Delete(ctx context.Context, name string, expectedValue string) error {
	entry, ok := m.names[name]
	if !ok {
		return ErrNotFound
	}
	if expectedValue != "" && entry.Value != expectedValue {
		return ErrPreconditionFailed
	}
	delete(m.names, name)
	return nil
}

func (m *mockParentNames) Lookup(ctx context.Context, id string) ([]string, error) {
	var results []string
	for k, v := range m.names {
		if v.Value == id {
			results = append(results, k)
		}
	}
	if results == nil {
		results = []string{}
	}
	return results, nil
}

func TestUpstreamNames_Get(t *testing.T) {
	ctx := context.Background()
	local := NewInMemoryNames()
	parent := newMockParent()

	upstream := NewUpstreamNames(local, parent)

	parent.Put(ctx, "test-name", "parent-value", []string{"tok1"})

	// 1. Name not in local but in parent
	entry, err := upstream.Get(ctx, "test-name")
	if err != nil {
		t.Fatalf("Expected test-name to be found in parent names: %v", err)
	}
	if entry.Value != "parent-value" {
		t.Errorf("Expected value parent-value, got %v", entry.Value)
	}

	// 2. The entry should now be cached in local
	localEntry, err := local.Get(ctx, "test-name")
	if err != nil {
		t.Fatalf("Expected test-name to be cached in local names: %v", err)
	}
	if localEntry.Value != "parent-value" {
		t.Errorf("Expected cached value parent-value, got %v", localEntry.Value)
	}
}

func TestUpstreamNames_PutDeleteIsolation(t *testing.T) {
	ctx := context.Background()
	local := NewInMemoryNames()
	parent := newMockParent()

	upstream := NewUpstreamNames(local, parent)

	// 1. Service registered to upstream directly ONLY goes to local
	err := upstream.Put(ctx, "local-only", "local-val", []string{})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	_, pErr := parent.Get(ctx, "local-only")
	if pErr == nil {
		t.Errorf("Expected parent to NOT register local-only name")
	}

	// 2. Delete applies strictly logically
	err = upstream.Delete(ctx, "local-only", "")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, lErr := local.Get(ctx, "local-only")
	if lErr != ErrNotFound {
		t.Fatalf("Expected local-only to be deleted locally")
	}
}

func TestUpstreamNames_Lookup(t *testing.T) {
	ctx := context.Background()

	// 1. With nil parent
	localOnly := NewInMemoryNames()
	localOnly.Put(ctx, "name1", "id-abc", []string{})
	upstream1 := NewUpstreamNames(localOnly, nil)
	results, err := upstream1.Lookup(ctx, "id-abc")
	if err != nil {
		t.Fatalf("unexpected Lookup error: %v", err)
	}
	if len(results) != 1 || results[0] != "name1" {
		t.Errorf("expected ['name1'], got %v", results)
	}

	// 2. With both local and parent
	local := NewInMemoryNames()
	parent := newMockParent()
	local.Put(ctx, "local-name", "target-id", []string{})
	local.Put(ctx, "shared-name", "target-id", []string{})
	parent.Put(ctx, "shared-name", "target-id", []string{})
	parent.Put(ctx, "parent-name", "target-id", []string{})

	upstream2 := NewUpstreamNames(local, parent)
	results, err = upstream2.Lookup(ctx, "target-id")
	if err != nil {
		t.Fatalf("unexpected Lookup error: %v", err)
	}

	// Should contain local-name, shared-name, parent-name once.
	if len(results) != 3 {
		t.Errorf("expected 3 combined lookup results, got %v", results)
	}
	seen := make(map[string]bool)
	for _, n := range results {
		seen[n] = true
	}
	if !seen["local-name"] || !seen["shared-name"] || !seen["parent-name"] {
		t.Errorf("missing expected names in combined lookup results: %v", results)
	}
}
