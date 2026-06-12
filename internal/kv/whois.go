package kv

import (
	"context"

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
