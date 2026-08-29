package repository

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"strings"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
)

// TailscaleClient defines the interface for querying Tailscale daemon identity.
type TailscaleClient interface {
	WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error)
	Status(ctx context.Context) (*ipnstate.Status, error)
}

// CurrentIdentity resolves the current user identity from the local Tailscale daemon,
// falling back to environment and OS user information if Tailscale is offline.
func CurrentIdentity(ctx context.Context) Identity {
	return ResolveIdentity(ctx, nil)
}

// ResolveIdentity resolves identity using the provided TailscaleClient (or default local client).
func ResolveIdentity(ctx context.Context, tsClient TailscaleClient) Identity {
	if tsClient == nil {
		tsClient = &local.Client{}
	}

	// Try querying local Tailscale daemon status first
	if status, err := tsClient.Status(ctx); err == nil && status != nil {
		if status.Self != nil && status.Self.UserID != 0 {
			if userProfile, ok := status.User[status.Self.UserID]; ok {
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
				}
			}
		}
	}

	// Fallback to local environment and OS user
	name := os.Getenv("GIT_AUTHOR_NAME")
	if name == "" {
		name = os.Getenv("USER")
	}
	if name == "" {
		if u, err := user.Current(); err == nil {
			name = u.Username
			if u.Name != "" {
				name = u.Name
			}
		}
	}
	if name == "" {
		name = "unknown"
	}

	email := os.Getenv("GIT_AUTHOR_EMAIL")
	if email == "" {
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "localhost"
		}
		email = fmt.Sprintf("%s@%s", strings.ToLower(name), hostname)
	}

	token := os.Getenv("INVARIANT_AUTH_TOKEN")

	return Identity{
		Name:  name,
		Email: email,
		Token: token,
	}
}

// WhoIsFromRemote resolves the identity of a remote HTTP caller using Tailscale WhoIs.
func WhoIsFromRemote(ctx context.Context, tsClient TailscaleClient, remoteAddr string) (*Identity, error) {
	if tsClient == nil {
		tsClient = &local.Client{}
	}

	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}

	whois, err := tsClient.WhoIs(ctx, remoteAddr)
	if err != nil {
		whois, err = tsClient.WhoIs(ctx, host)
	}
	if err != nil {
		return nil, fmt.Errorf("whois lookup failed for %s: %w", remoteAddr, err)
	}

	if whois.UserProfile == nil {
		return nil, fmt.Errorf("no user profile returned for %s", remoteAddr)
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
