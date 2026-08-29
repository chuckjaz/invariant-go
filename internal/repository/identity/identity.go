// Package identity provides user identity models and extensible identity provider
// interfaces and implementations (e.g. Tailscale, Environment/OS).
package identity

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strings"
	"sync"
)

// Identity captures user identity and authorization credentials.
type Identity struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	Token string `json:"token,omitempty"`
}

// Provider abstracts user identity and authorization lookup across different backends.
type Provider interface {
	// CurrentIdentity resolves the local active user's identity.
	CurrentIdentity(ctx context.Context) (Identity, error)

	// IdentityFromRemote resolves caller identity given their network address or credentials.
	IdentityFromRemote(ctx context.Context, remoteAddr string) (*Identity, error)
}

// EnvironmentProvider resolves user identity from environment variables and OS user information.
type EnvironmentProvider struct{}

// NewEnvironmentProvider creates a Provider based on local OS and environment variables.
func NewEnvironmentProvider() *EnvironmentProvider {
	return &EnvironmentProvider{}
}

// CurrentIdentity resolves identity from GIT_AUTHOR_NAME/EMAIL, USER, and os/user.Current.
func (p *EnvironmentProvider) CurrentIdentity(ctx context.Context) (Identity, error) {
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

// IdentityFromRemote returns an error as EnvironmentProvider does not resolve remote network callers.
func (p *EnvironmentProvider) IdentityFromRemote(ctx context.Context, remoteAddr string) (*Identity, error) {
	return nil, fmt.Errorf("environment identity provider does not support remote address resolution")
}

// MultiProvider chains multiple Providers in priority order.
type MultiProvider struct {
	providers []Provider
}

// NewMultiProvider creates a provider that evaluates the given providers in sequence.
func NewMultiProvider(providers ...Provider) *MultiProvider {
	return &MultiProvider{providers: providers}
}

// CurrentIdentity queries providers in order until one returns a non-empty identity with name.
func (m *MultiProvider) CurrentIdentity(ctx context.Context) (Identity, error) {
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
func (m *MultiProvider) IdentityFromRemote(ctx context.Context, remoteAddr string) (*Identity, error) {
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
	defaultProvider   Provider
)

func init() {
	defaultProvider = NewMultiProvider(
		NewTailscaleProvider(nil),
		NewEnvironmentProvider(),
	)
}

// DefaultProvider returns the current global Provider.
func DefaultProvider() Provider {
	defaultProviderMu.RLock()
	defer defaultProviderMu.RUnlock()
	return defaultProvider
}

// SetDefaultProvider sets the global Provider.
func SetDefaultProvider(p Provider) {
	defaultProviderMu.Lock()
	defer defaultProviderMu.Unlock()
	defaultProvider = p
}

// CurrentIdentity resolves the active user's identity using the default provider chain.
func CurrentIdentity(ctx context.Context) Identity {
	id, err := DefaultProvider().CurrentIdentity(ctx)
	if err != nil {
		return Identity{Name: "unknown"}
	}
	return id
}
