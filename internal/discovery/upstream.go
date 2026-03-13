package discovery

import (
	"context"
)

// Assert that UpstreamDiscovery implements the Discovery interface.
var _ Discovery = (*UpstreamDiscovery)(nil)

// UpstreamDiscovery delegates queries to a parent discovery service
// if they are not found in the local cache/registry.
type UpstreamDiscovery struct {
	local  *InMemoryDiscovery
	parent Discovery
}

// NewUpstreamDiscovery creates a new discovery proxy that falls back
// to parent for missing services and caches them locally.
func NewUpstreamDiscovery(local *InMemoryDiscovery, parent Discovery) *UpstreamDiscovery {
	return &UpstreamDiscovery{
		local:  local,
		parent: parent,
	}
}

// Get checks the local registry first. If not found, it asks the parent
// and caches the result locally on success.
func (u *UpstreamDiscovery) Get(ctx context.Context, id string) (ServiceDescription, bool) {
	desc, ok := u.local.Get(ctx, id)
	if ok {
		return desc, true
	}

	if u.parent != nil {
		desc, ok = u.parent.Get(ctx, id)
		if ok {
			// Cache locally
			_ = u.local.Register(ctx, ServiceRegistration{
				ID:        desc.ID,
				Address:   desc.Address,
				Protocols: desc.Protocols,
			})
			return desc, true
		}
	}

	return ServiceDescription{}, false
}

// Find queries local services. If it receives fewer than `count` results,
// it delegates the remaining needed count to the parent, appends the results,
// and caches the parent hits locally.
func (u *UpstreamDiscovery) Find(ctx context.Context, protocol string, count int) ([]ServiceDescription, error) {
	localResults, err := u.local.Find(ctx, protocol, count)
	if err != nil {
		return nil, err
	}

	if len(localResults) >= count || u.parent == nil {
		return localResults, nil
	}

	remaining := count - len(localResults)
	parentResults, err := u.parent.Find(ctx, protocol, remaining)
	if err != nil {
		// Logically we can return local results rather than hard failing,
		// but standard Go patterns return the err.
		return nil, err
	}

	for _, pDesc := range parentResults {
		// Deduping just in case local had it mapped under a different query or concurrent mutations
		alreadyExists := false
		for _, lDesc := range localResults {
			if lDesc.ID == pDesc.ID {
				alreadyExists = true
				break
			}
		}

		if !alreadyExists {
			localResults = append(localResults, pDesc)
			// Cache locally
			_ = u.local.Register(ctx, ServiceRegistration{
				ID:        pDesc.ID,
				Address:   pDesc.Address,
				Protocols: pDesc.Protocols,
			})
		}
	}

	return localResults, nil
}

// Register registers the service only to the local registry.
// Changes are intentionally NOT propagated to the parent.
func (u *UpstreamDiscovery) Register(ctx context.Context, reg ServiceRegistration) error {
	return u.local.Register(ctx, reg)
}
