package repository

import (
	"context"
	"os"
	"testing"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

type mockTailscaleClient struct {
	statusResp *ipnstate.Status
	whoisResp  *apitype.WhoIsResponse
	statusErr  error
	whoisErr   error
}

func (m *mockTailscaleClient) Status(ctx context.Context) (*ipnstate.Status, error) {
	return m.statusResp, m.statusErr
}

func (m *mockTailscaleClient) WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error) {
	return m.whoisResp, m.whoisErr
}

func TestResolveIdentityWithTailscale(t *testing.T) {
	ctx := context.Background()
	privKey := key.NewNode()
	nodeKey := privKey.Public()

	mockTS := &mockTailscaleClient{
		statusResp: &ipnstate.Status{
			Self: &ipnstate.PeerStatus{
				UserID:    tailcfg.UserID(1001),
				PublicKey: nodeKey,
			},
			User: map[tailcfg.UserID]tailcfg.UserProfile{
				tailcfg.UserID(1001): {
					ID:          tailcfg.UserID(1001),
					LoginName:   "developer@tailscale.net",
					DisplayName: "Lead Developer",
				},
			},
		},
	}

	identity := ResolveIdentity(ctx, mockTS)
	if identity.Name != "Lead Developer" {
		t.Errorf("Expected name 'Lead Developer', got %q", identity.Name)
	}
	if identity.Email != "developer@tailscale.net" {
		t.Errorf("Expected email 'developer@tailscale.net', got %q", identity.Email)
	}
	if identity.Token != nodeKey.String() {
		t.Errorf("Expected token %q, got %q", nodeKey.String(), identity.Token)
	}
}

func TestResolveIdentityFallback(t *testing.T) {
	ctx := context.Background()
	os.Setenv("GIT_AUTHOR_NAME", "Test Author")
	os.Setenv("GIT_AUTHOR_EMAIL", "author@example.com")
	os.Setenv("INVARIANT_AUTH_TOKEN", "fallback-token")
	defer func() {
		os.Unsetenv("GIT_AUTHOR_NAME")
		os.Unsetenv("GIT_AUTHOR_EMAIL")
		os.Unsetenv("INVARIANT_AUTH_TOKEN")
	}()

	mockTS := &mockTailscaleClient{
		statusResp: nil,
	}

	identity := ResolveIdentity(ctx, mockTS)
	if identity.Name != "Test Author" {
		t.Errorf("Expected name 'Test Author', got %q", identity.Name)
	}
	if identity.Email != "author@example.com" {
		t.Errorf("Expected email 'author@example.com', got %q", identity.Email)
	}
	if identity.Token != "fallback-token" {
		t.Errorf("Expected token 'fallback-token', got %q", identity.Token)
	}
}

func TestWhoIsFromRemote(t *testing.T) {
	ctx := context.Background()
	privKey := key.NewNode()
	nodeKey := privKey.Public()

	mockTS := &mockTailscaleClient{
		whoisResp: &apitype.WhoIsResponse{
			UserProfile: &tailcfg.UserProfile{
				LoginName:   "peer@tailscale.net",
				DisplayName: "Peer Reviewer",
			},
			Node: &tailcfg.Node{
				Key: nodeKey,
			},
		},
	}

	identity, err := WhoIsFromRemote(ctx, mockTS, "100.64.0.1:12345")
	if err != nil {
		t.Fatalf("WhoIsFromRemote failed: %v", err)
	}
	if identity.Name != "Peer Reviewer" {
		t.Errorf("Expected name 'Peer Reviewer', got %q", identity.Name)
	}
	if identity.Email != "peer@tailscale.net" {
		t.Errorf("Expected email 'peer@tailscale.net', got %q", identity.Email)
	}
	if identity.Token != nodeKey.String() {
		t.Errorf("Expected token %q, got %q", nodeKey.String(), identity.Token)
	}
}
