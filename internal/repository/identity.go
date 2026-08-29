package repository

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strings"
	"sync"
)

// IdentityProvider abstracts user identity and authorization lookup across different backends
// (such as Tailscale, Environment/OS, LDAP, OAuth, or SSH keys).
type IdentityProvider interface {
	// CurrentIdentity resolves the local active user's identity.
	CurrentIdentity(ctx context.Context) (Identity, error)

	// IdentityFromRemote resolves caller identity given their network address or credentials.
	IdentityFromRemote(ctx context.Context, remoteAddr string) (*Identity, error)
}

// EnvironmentIdentityProvider resolves user identity from environment variables and OS user information.
type EnvironmentIdentityProvider struct{}

// NewEnvironmentIdentityProvider creates an IdentityProvider based on local OS and environment variables.
func NewEnvironmentIdentityProvider() *EnvironmentIdentityProvider {
	return &EnvironmentIdentityProvider{}
}

// CurrentIdentity resolves identity from GIT_AUTHOR_NAME/EMAIL, USER, and os/user.Current.
func (p *EnvironmentIdentityProvider) CurrentIdentity(ctx context.Context) (Identity, error) {
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
	}, nil
}

// IdentityFromRemote returns an error as EnvironmentIdentityProvider does not resolve remote network callers.
func (p *EnvironmentIdentityProvider) IdentityFromRemote(ctx context.Context, remoteAddr string) (*Identity, error) {
	return nil, fmt.Errorf("environment identity provider does not support remote address resolution")
}

// MultiIdentityProvider chains multiple IdentityProviders in priority order.
type MultiIdentityProvider struct {
	providers []IdentityProvider
}

// NewMultiIdentityProvider creates a provider that evaluates the given providers in sequence.
func NewMultiIdentityProvider(providers ...IdentityProvider) *MultiIdentityProvider {
	return &MultiIdentityProvider{providers: providers}
}

// CurrentIdentity queries providers in order until one returns a non-empty identity with name.
func (m *MultiIdentityProvider) CurrentIdentity(ctx context.Context) (Identity, error) {
	var lastErr error
	for _, p := range m.providers {
		id, err := p.CurrentIdentity(ctx)
		if err == nil && id.Name != "" && id.Name != "unknown" {
			return id, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	// If all returned unknown/error, try fallback
	for _, p := range m.providers {
		id, err := p.CurrentIdentity(ctx)
		if err == nil && id.Name != "" {
			return id, nil
		}
	}
	if lastErr != nil {
		return Identity{}, lastErr
	}
	return Identity{Name: "unknown"}, nil
}

// IdentityFromRemote queries providers in order until one resolves the remote address.
func (m *MultiIdentityProvider) IdentityFromRemote(ctx context.Context, remoteAddr string) (*Identity, error) {
	var lastErr error
	for _, p := range m.providers {
		id, err := p.IdentityFromRemote(ctx, remoteAddr)
		if err == nil && id != nil {
			return id, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("unable to resolve remote identity for %s from any provider", remoteAddr)
}

var (
	defaultProviderMu sync.RWMutex
	defaultProvider   IdentityProvider
)

func init() {
	defaultProvider = NewMultiIdentityProvider(
		NewTailscaleIdentityProvider(nil),
		NewEnvironmentIdentityProvider(),
	)
}

// DefaultIdentityProvider returns the current global IdentityProvider.
func DefaultIdentityProvider() IdentityProvider {
	defaultProviderMu.RLock()
	defer defaultProviderMu.RUnlock()
	return defaultProvider
}

// SetDefaultIdentityProvider sets the global IdentityProvider.
func SetDefaultIdentityProvider(p IdentityProvider) {
	defaultProviderMu.Lock()
	defer defaultProviderMu.Unlock()
	defaultProvider = p
}

// CurrentIdentity resolves the active user's identity using the default provider chain.
func CurrentIdentity(ctx context.Context) Identity {
	id, err := DefaultIdentityProvider().CurrentIdentity(ctx)
	if err != nil {
		return Identity{Name: "unknown"}
	}
	return id
}
