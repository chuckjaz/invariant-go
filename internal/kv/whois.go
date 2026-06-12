package kv

import (
	"context"
	"sync"
	"time"

	"tailscale.com/client/tailscale/apitype"
)

// TailscaleClient defines the interface for Tailscale WhoIs lookups.
type TailscaleClient interface {
	WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error)
}

type whoIsKeyType struct{}

var whoIsKey = whoIsKeyType{}

// ContextWithWhoIs returns a new context with the provided WhoIsResponse value.
func ContextWithWhoIs(ctx context.Context, whois *apitype.WhoIsResponse) context.Context {
	return context.WithValue(ctx, whoIsKey, whois)
}

// WhoIsFromContext retrieves the WhoIsResponse value from the context, if present.
func WhoIsFromContext(ctx context.Context) (*apitype.WhoIsResponse, bool) {
	whois, ok := ctx.Value(whoIsKey).(*apitype.WhoIsResponse)
	return whois, ok
}

type cacheEntry struct {
	whois     *apitype.WhoIsResponse
	err       error
	createdAt time.Time
}

type whoIsCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

func newWhoIsCache() *whoIsCache {
	return &whoIsCache{
		entries: make(map[string]cacheEntry),
	}
}

func (c *whoIsCache) Get(remoteAddr string, ttl time.Duration) (*apitype.WhoIsResponse, error, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[remoteAddr]
	if !ok || time.Since(entry.createdAt) > ttl {
		return nil, nil, false
	}
	return entry.whois, entry.err, true
}

func (c *whoIsCache) Set(remoteAddr string, whois *apitype.WhoIsResponse, err error, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Prune expired entries
	now := time.Now()
	for addr, entry := range c.entries {
		if now.Sub(entry.createdAt) > ttl {
			delete(c.entries, addr)
		}
	}

	c.entries[remoteAddr] = cacheEntry{
		whois:     whois,
		err:       err,
		createdAt: now,
	}
}
