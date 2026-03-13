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
