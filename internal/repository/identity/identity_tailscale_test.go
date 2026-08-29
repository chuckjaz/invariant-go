package identity

import (
	"context"
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

func TestTailscaleProvider_CurrentIdentity(t *testing.T) {
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

	provider := NewTailscaleProvider(mockTS)
	id, err := provider.CurrentIdentity(ctx)
	if err != nil {
		t.Fatalf("CurrentIdentity failed: %v", err)
	}
	if id.Name != "Lead Developer" {
		t.Errorf("Expected name 'Lead Developer', got %q", id.Name)
	}
	if id.Email != "developer@tailscale.net" {
		t.Errorf("Expected email 'developer@tailscale.net', got %q", id.Email)
	}
	if id.Token != nodeKey.String() {
		t.Errorf("Expected token %q, got %q", nodeKey.String(), id.Token)
	}
}

func TestTailscaleProvider_IdentityFromRemote(t *testing.T) {
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

	provider := NewTailscaleProvider(mockTS)
	id, err := provider.IdentityFromRemote(ctx, "100.64.0.1:12345")
	if err != nil {
		t.Fatalf("IdentityFromRemote failed: %v", err)
	}
	if id.Name != "Peer Reviewer" {
		t.Errorf("Expected name 'Peer Reviewer', got %q", id.Name)
	}
	if id.Email != "peer@tailscale.net" {
		t.Errorf("Expected email 'peer@tailscale.net', got %q", id.Email)
	}
	if id.Token != nodeKey.String() {
		t.Errorf("Expected token %q, got %q", nodeKey.String(), id.Token)
	}
}
