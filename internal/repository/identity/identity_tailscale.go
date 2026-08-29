package identity

import (
	"context"
	"fmt"
	"net"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
)

// TailscaleClient defines the interface for querying Tailscale daemon identity.
type TailscaleClient interface {
	WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error)
	Status(ctx context.Context) (*ipnstate.Status, error)
}

// TailscaleProvider implements Provider using the local Tailscale daemon.
type TailscaleProvider struct {
	client TailscaleClient
}

// NewTailscaleProvider creates a TailscaleProvider with the specified client.
// If client is nil, the standard local.Client is used.
func NewTailscaleProvider(client TailscaleClient) *TailscaleProvider {
	if client == nil {
		client = &local.Client{}
	}
	return &TailscaleProvider{client: client}
}

// CurrentIdentity resolves the local user's identity from Tailscale Status.
func (t *TailscaleProvider) CurrentIdentity(ctx context.Context) (Identity, error) {
	status, err := t.client.Status(ctx)
	if err != nil {
		return Identity{}, fmt.Errorf("tailscale status check failed: %w", err)
	}
	if status == nil || status.Self == nil || status.Self.UserID == 0 {
		return Identity{}, fmt.Errorf("tailscale node unauthenticated or offline")
	}

	userProfile, ok := status.User[status.Self.UserID]
	if !ok {
		return Identity{}, fmt.Errorf("no user profile for tailscale user id %d", status.Self.UserID)
	}

	displayName := userProfile.DisplayName
	loginName := userProfile.LoginName
	if displayName == "" {
		displayName = loginName
	}
	token := ""
	if !status.Self.PublicKey.IsZero() {
		token = status.Self.PublicKey.String()
	}

	return Identity{
		Name:  displayName,
		Email: loginName,
		Token: token,
	}, nil
}

// IdentityFromRemote resolves caller identity using Tailscale WhoIs.
func (t *TailscaleProvider) IdentityFromRemote(ctx context.Context, remoteAddr string) (*Identity, error) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}

	whois, err := t.client.WhoIs(ctx, remoteAddr)
	if err != nil {
		whois, err = t.client.WhoIs(ctx, host)
	}
	if err != nil {
		return nil, fmt.Errorf("tailscale whois lookup failed for %s: %w", remoteAddr, err)
	}

	if whois == nil || whois.UserProfile == nil {
		return nil, fmt.Errorf("no tailscale user profile returned for %s", remoteAddr)
	}

	displayName := whois.UserProfile.DisplayName
	if displayName == "" {
		displayName = whois.UserProfile.LoginName
	}

	token := ""
	if whois.Node != nil && !whois.Node.Key.IsZero() {
		token = whois.Node.Key.String()
	}

	return &Identity{
		Name:  displayName,
		Email: whois.UserProfile.LoginName,
		Token: token,
	}, nil
}
