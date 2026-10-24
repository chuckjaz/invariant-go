package identity

import (
	"context"
	"fmt"

	"github.com/chuckjaz/invariant-go/internal/discovery"
	"tailscale.com/types/key"
)

// ITPPeerKeyResolver extracts the peer's public key from a remote address.
type ITPPeerKeyResolver interface {
	PeerKey(remoteAddr string) (key.NodePublic, bool)
}

// ITPProvider implements Provider using the ITP / Discovery ControlPlane.
type ITPProvider struct {
	controlPlane discovery.ControlPlane
	resolver     ITPPeerKeyResolver
	selfKey      key.NodePublic
	selfIdentity *discovery.NodeIdentity
}

// ITPProviderOption configures an ITPProvider.
type ITPProviderOption func(*ITPProvider)

// WithITPResolver configures the peer key resolver (e.g. *itp.Listener).
func WithITPResolver(r ITPPeerKeyResolver) ITPProviderOption {
	return func(p *ITPProvider) {
		p.resolver = r
	}
}

// WithSelfKey configures the local node's public key and identity.
func WithSelfKey(k key.NodePublic, ident *discovery.NodeIdentity) ITPProviderOption {
	return func(p *ITPProvider) {
		p.selfKey = k
		p.selfIdentity = ident
	}
}

// NewITPProvider creates a new ITPProvider.
func NewITPProvider(cp discovery.ControlPlane, opts ...ITPProviderOption) *ITPProvider {
	p := &ITPProvider{
		controlPlane: cp,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// CurrentIdentity resolves the local user's identity.
func (p *ITPProvider) CurrentIdentity(ctx context.Context) (Identity, error) {
	if p.selfIdentity != nil {
		token := ""
		if !p.selfKey.IsZero() {
			token = p.selfKey.String()
		}
		return Identity{
			Name:  p.selfIdentity.Name,
			Email: p.selfIdentity.Email,
			Token: token,
		}, nil
	}

	if !p.selfKey.IsZero() && p.controlPlane != nil {
		ok, ident, err := p.controlPlane.VerifyKey(ctx, p.selfKey.String())
		if err == nil && ok && ident != nil {
			return Identity{
				Name:  ident.Name,
				Email: ident.Email,
				Token: p.selfKey.String(),
			}, nil
		}
	}

	return Identity{}, fmt.Errorf("no self identity configured for itp provider")
}

// IdentityFromRemote resolves caller identity using the peer key and ControlPlane.
func (p *ITPProvider) IdentityFromRemote(ctx context.Context, remoteAddr string) (*Identity, error) {
	if p.controlPlane == nil {
		return nil, fmt.Errorf("no control plane configured")
	}

	var pubKeyStr string
	if p.resolver != nil {
		k, ok := p.resolver.PeerKey(remoteAddr)
		if ok {
			pubKeyStr = k.String()
		}
	}

	if pubKeyStr == "" {
		pubKeyStr = remoteAddr
	}

	ok, ident, err := p.controlPlane.VerifyKey(ctx, pubKeyStr)
	if err != nil {
		return nil, fmt.Errorf("failed to verify itp peer key %q: %w", pubKeyStr, err)
	}
	if !ok || ident == nil {
		return nil, fmt.Errorf("itp peer key %q not authorized", pubKeyStr)
	}

	return &Identity{
		Name:  ident.Name,
		Email: ident.Email,
		Token: pubKeyStr,
	}, nil
}
