package names

import (
	"context"
)

// Assert that UpstreamNames implements the Names interface.
var _ Names = (*UpstreamNames)(nil)

// UpstreamNames delegates queries to a parent names service
// if they are not found in the local cache/registry.
type UpstreamNames struct {
	local  Names
	parent Names
}

// NewUpstreamNames creates a new names proxy that falls back
// to parent for missing names and caches them locally.
func NewUpstreamNames(local Names, parent Names) *UpstreamNames {
	return &UpstreamNames{
		local:  local,
		parent: parent,
	}
}

// Get checks the local registry first. If ErrNotFound, it asks the parent
// and caches the result locally on success.
func (u *UpstreamNames) Get(ctx context.Context, name string) (NameEntry, error) {
	entry, err := u.local.Get(ctx, name)
	if err == nil {
		return entry, nil
	}

	if err == ErrNotFound && u.parent != nil {
		parentEntry, pErr := u.parent.Get(ctx, name)
		if pErr == nil {
			// Cache locally
			_ = u.local.Put(ctx, name, parentEntry.Value, parentEntry.Tokens)
			return parentEntry, nil
		}
		return NameEntry{}, pErr
	}

	return NameEntry{}, err
}

// Put registers the name only to the local registry.
// Changes are intentionally NOT propagated to the parent.
func (u *UpstreamNames) Put(ctx context.Context, name string, value string, tokens []string) error {
	return u.local.Put(ctx, name, value, tokens)
}

// Delete removes the name only from the local registry.
func (u *UpstreamNames) Delete(ctx context.Context, name string, expectedValue string) error {
	return u.local.Delete(ctx, name, expectedValue)
}

// Lookup checks both local and parent registries for the given ID.
func (u *UpstreamNames) Lookup(ctx context.Context, id string) ([]string, error) {
	localResults, err := u.local.Lookup(ctx, id)
	if err != nil {
		return nil, err
	}

	if u.parent == nil {
		return localResults, nil
	}

	parentResults, err := u.parent.Lookup(ctx, id)
	if err != nil {
		return localResults, err
	}

	seen := make(map[string]bool)
	for _, name := range localResults {
		seen[name] = true
	}

	combined := localResults
	for _, name := range parentResults {
		if !seen[name] {
			combined = append(combined, name)
		}
	}

	return combined, nil
}
