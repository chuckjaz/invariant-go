package identity

import (
	"context"
	"testing"

	"github.com/chuckjaz/invariant-go/internal/discovery"
	"tailscale.com/types/key"
)

type mockControlPlane struct {
	registerFunc  func(ctx context.Context, pubKey string, ident discovery.NodeIdentity) error
	verifyKeyFunc func(ctx context.Context, pubKey string) (bool, *discovery.NodeIdentity, error)
	authKeysFunc  func(ctx context.Context) ([]string, error)
}

func (m *mockControlPlane) RegisterClient(ctx context.Context, pubKey string, ident discovery.NodeIdentity) error {
	if m.registerFunc != nil {
		return m.registerFunc(ctx, pubKey, ident)
	}
	return nil
}

func (m *mockControlPlane) VerifyKey(ctx context.Context, pubKey string) (bool, *discovery.NodeIdentity, error) {
	if m.verifyKeyFunc != nil {
		return m.verifyKeyFunc(ctx, pubKey)
	}
	return false, nil, nil
}

func (m *mockControlPlane) AuthorizedKeys(ctx context.Context) ([]string, error) {
	if m.authKeysFunc != nil {
		return m.authKeysFunc(ctx)
	}
	return nil, nil
}

type mockPeerResolver struct {
	key key.NodePublic
	ok  bool
}

func (m *mockPeerResolver) PeerKey(remoteAddr string) (key.NodePublic, bool) {
	return m.key, m.ok
}

func TestITPProvider_CurrentIdentity(t *testing.T) {
	ctx := context.Background()
	privKey := key.NewNode()
	nodeKey := privKey.Public()

	mockCP := &mockControlPlane{
		verifyKeyFunc: func(ctx context.Context, pubKey string) (bool, *discovery.NodeIdentity, error) {
			if pubKey == nodeKey.String() {
				return true, &discovery.NodeIdentity{
					Name:  "Alice",
					Email: "alice@example.com",
				}, nil
			}
			return false, nil, nil
		},
	}

	p := NewITPProvider(mockCP, WithSelfKey(nodeKey, nil))
	id, err := p.CurrentIdentity(ctx)
	if err != nil {
		t.Fatalf("CurrentIdentity failed: %v", err)
	}
	if id.Name != "Alice" {
		t.Errorf("Expected name 'Alice', got %q", id.Name)
	}
	if id.Email != "alice@example.com" {
		t.Errorf("Expected email 'alice@example.com', got %q", id.Email)
	}
	if id.Token != nodeKey.String() {
		t.Errorf("Expected token %q, got %q", nodeKey.String(), id.Token)
	}
}

func TestITPProvider_IdentityFromRemote(t *testing.T) {
	ctx := context.Background()
	privKey := key.NewNode()
	nodeKey := privKey.Public()

	mockCP := &mockControlPlane{
		verifyKeyFunc: func(ctx context.Context, pubKey string) (bool, *discovery.NodeIdentity, error) {
			if pubKey == nodeKey.String() {
				return true, &discovery.NodeIdentity{
					Name:  "Bob",
					Email: "bob@example.com",
				}, nil
			}
			return false, nil, nil
		},
	}

	resolver := &mockPeerResolver{
		key: nodeKey,
		ok:  true,
	}

	p := NewITPProvider(mockCP, WithITPResolver(resolver))
	id, err := p.IdentityFromRemote(ctx, "127.0.0.1:54321")
	if err != nil {
		t.Fatalf("IdentityFromRemote failed: %v", err)
	}
	if id.Name != "Bob" {
		t.Errorf("Expected name 'Bob', got %q", id.Name)
	}
	if id.Email != "bob@example.com" {
		t.Errorf("Expected email 'bob@example.com', got %q", id.Email)
	}
	if id.Token != nodeKey.String() {
		t.Errorf("Expected token %q, got %q", nodeKey.String(), id.Token)
	}
}
