package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"tailscale.com/types/key"
)

type mockPeerKeyResolver struct {
	mu      sync.Mutex
	peerKey key.NodePublic
	ok      bool
}

func (m *mockPeerKeyResolver) SetPeer(keyStr string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if keyStr == "" {
		m.peerKey = key.NodePublic{}
		m.ok = false
		return
	}
	var pub key.NodePublic
	if err := pub.UnmarshalText([]byte(keyStr)); err != nil {
		panic(err)
	}
	m.peerKey = pub
	m.ok = ok
}

func (m *mockPeerKeyResolver) PeerKey(remoteAddr string) (key.NodePublic, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peerKey, m.ok
}

func TestInvitationLifecycleAndSingleUse(t *testing.T) {
	d := NewInMemoryDiscovery().WithControlPlaneURL("itp://tc1example")
	ctx := context.Background()

	// 1. Create invitation
	inv, err := d.CreateInvitation(ctx, "storage-1", RoleService, NodeIdentity{Name: "admin"}, 5*time.Minute)
	if err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}
	if inv.Code == "" || inv.Role != RoleService || inv.ControlPlaneURL != "itp://tc1example" {
		t.Fatalf("unexpected invitation: %+v", inv)
	}
	rawCode, cpURL := ParseInvitationCode(inv.Code)
	if cpURL != "itp://tc1example" || rawCode == "" {
		t.Fatalf("expected embedded control plane URL in invitation code %q, got raw=%q cpURL=%q", inv.Code, rawCode, cpURL)
	}

	// 2. Redeem once -> succeeds
	pubKey := "nodekey:1111111111111111111111111111111111111111111111111111111111111111"
	tok, err := d.RedeemInvitation(ctx, inv.Code, pubKey, NodeIdentity{Name: "storage-node-1"})
	if err != nil {
		t.Fatalf("RedeemInvitation failed: %v", err)
	}
	if tok.Token == "" || tok.Role != RoleService || tok.PublicKey != pubKey {
		t.Fatalf("unexpected access token record: %+v", tok)
	}

	// Verify key is immediately authorized in ControlPlane
	ok, _, err := d.VerifyKey(ctx, pubKey)
	if err != nil || !ok {
		t.Fatalf("expected redeemed public key to be authorized, got ok=%v err=%v", ok, err)
	}

	// 3. Second redemption of same invite -> fails with ErrInvitationUsed
	_, err = d.RedeemInvitation(ctx, inv.Code, pubKey, NodeIdentity{Name: "storage-node-2"})
	if !errors.Is(err, ErrInvitationUsed) {
		t.Fatalf("expected ErrInvitationUsed on second redemption, got %v", err)
	}

	// 4. Expired invitation -> fails with ErrInvitationExpired
	expInv, _ := GenerateInvitation("expired-node", RoleUser, NodeIdentity{Name: "admin"}, 5*time.Minute)
	expInv.ExpiresAt = time.Now().UTC().Add(-1 * time.Minute)
	_ = d.RestoreInvitation(ctx, *expInv)
	_, err = d.RedeemInvitation(ctx, expInv.Code, pubKey, NodeIdentity{Name: "user-1"})
	if !errors.Is(err, ErrInvitationExpired) {
		t.Fatalf("expected ErrInvitationExpired, got %v", err)
	}

	// 5. Revoked invitation -> fails with ErrInvitationRevoked
	revInv, err := d.CreateInvitation(ctx, "revoked-node", RoleUser, NodeIdentity{Name: "admin"}, 5*time.Minute)
	if err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}
	if err := d.RevokeInvitation(ctx, revInv.ID, NodeIdentity{Name: "admin"}); err != nil {
		t.Fatalf("RevokeInvitation failed: %v", err)
	}
	_, err = d.RedeemInvitation(ctx, revInv.Code, pubKey, NodeIdentity{Name: "user-1"})
	if !errors.Is(err, ErrInvitationRevoked) {
		t.Fatalf("expected ErrInvitationRevoked, got %v", err)
	}
}

func TestServerRolesPinningAndRevocation(t *testing.T) {
	d := NewInMemoryDiscovery()
	peerResolver := &mockPeerKeyResolver{}
	srv := NewDiscoveryServer(d).
		WithRequireAuth(true).
		WithPeerKeyResolver(peerResolver)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx := context.Background()

	const (
		adminKey   = "nodekey:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		serviceKey = "nodekey:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		userKey    = "nodekey:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		rogueKey   = "nodekey:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	)

	// 1. Bootstrap an admin invitation directly on the store
	bootstrapFile := filepath.Join(t.TempDir(), "bootstrap.invite")
	adminInvite, created, err := EnsureBootstrapInvitation(ctx, d, 15*time.Minute, bootstrapFile)
	if err != nil || !created || adminInvite == nil {
		t.Fatalf("EnsureBootstrapInvitation failed: created=%v err=%v", created, err)
	}

	// 2. Unauthenticated request to /find must be rejected with 401
	unauthClient := NewClient(ts.URL, ts.Client())
	if _, err := unauthClient.Find(ctx, "", "", 10); err == nil {
		t.Fatalf("expected unauthenticated Find to fail")
	}

	// 3. Attempting to redeem adminInvite from a mismatched WireGuard peer key must fail (403)
	peerResolver.SetPeer(rogueKey, true)
	if _, err := unauthClient.RedeemInvitation(ctx, adminInvite.Code, adminKey, NodeIdentity{Name: "admin-cli"}); err == nil {
		t.Fatalf("expected RedeemInvitation with mismatched peer key to fail")
	}

	// 4. Redeem adminInvite from matching WireGuard peer key -> succeeds
	peerResolver.SetPeer(adminKey, true)
	adminTokRec, err := unauthClient.RedeemInvitation(ctx, adminInvite.Code, adminKey, NodeIdentity{Name: "admin-cli"})
	if err != nil {
		t.Fatalf("expected admin RedeemInvitation to succeed: %v", err)
	}
	adminClient := NewClient(ts.URL, ts.Client()).WithAccessToken(adminTokRec.Token)

	// EnsureBootstrapInvitation should now return nil since an active admin token exists
	secondBootstrap, created, err := EnsureBootstrapInvitation(ctx, d, 15*time.Minute, bootstrapFile)
	if err != nil || created || secondBootstrap != nil {
		t.Fatalf("expected EnsureBootstrapInvitation to be a no-op once admin exists, got %+v, created=%v, err=%v", secondBootstrap, created, err)
	}

	// 5. Using admin token from a different WireGuard peer key (rogueKey) must be rejected (401)
	peerResolver.SetPeer(rogueKey, true)
	if _, err := adminClient.Find(ctx, "", "", 10); err == nil {
		t.Fatalf("expected stolen token used from rogue peer key to be rejected")
	}

	// 6. Admin creates a service invitation and a user invitation
	peerResolver.SetPeer(adminKey, true)
	svcInvite, err := adminClient.CreateInvitation(ctx, "storage-svc", RoleService, NodeIdentity{}, 10*time.Minute)
	if err != nil {
		t.Fatalf("admin CreateInvitation (service) failed: %v", err)
	}
	usrInvite, err := adminClient.CreateInvitation(ctx, "alice", RoleUser, NodeIdentity{}, 10*time.Minute)
	if err != nil {
		t.Fatalf("admin CreateInvitation (user) failed: %v", err)
	}

	// 7. Service redeems service invitation
	peerResolver.SetPeer(serviceKey, true)
	svcTokRec, err := unauthClient.RedeemInvitation(ctx, svcInvite.Code, serviceKey, NodeIdentity{Name: "storage-svc"})
	if err != nil {
		t.Fatalf("service RedeemInvitation failed: %v", err)
	}
	svcClient := NewClient(ts.URL, ts.Client()).WithAccessToken(svcTokRec.Token)

	// Service can register itself with its own public key
	svcID := "1111111111111111111111111111111111111111111111111111111111111111"
	err = svcClient.Register(ctx, ServiceRegistration{
		ID:        svcID,
		Address:   "http://" + svcID + ":4000",
		Protocols: []string{"storage-v1"},
		PublicKey: serviceKey,
	})
	if err != nil {
		t.Fatalf("expected service role to register service: %v", err)
	}

	// Service cannot register with a spoofed public key
	err = svcClient.Register(ctx, ServiceRegistration{
		ID:        svcID,
		Address:   "http://" + svcID + ":4000",
		Protocols: []string{"storage-v1"},
		PublicKey: rogueKey,
	})
	if err == nil {
		t.Fatalf("expected service registration with mismatched public key to fail")
	}

	// Service cannot create invitations (admin only)
	if _, err := svcClient.CreateInvitation(ctx, "unauthorized", RoleUser, NodeIdentity{}, 5*time.Minute); err == nil {
		t.Fatalf("expected service role to be forbidden from creating invitations")
	}

	// 8. User redeems user invitation
	peerResolver.SetPeer(userKey, true)
	usrTokRec, err := unauthClient.RedeemInvitation(ctx, usrInvite.Code, userKey, NodeIdentity{Name: "alice"})
	if err != nil {
		t.Fatalf("user RedeemInvitation failed: %v", err)
	}
	usrClient := NewClient(ts.URL, ts.Client()).WithAccessToken(usrTokRec.Token)

	// User can query services
	found, err := usrClient.Find(ctx, "storage-v1", "", 10)
	if err != nil || len(found) != 1 {
		t.Fatalf("expected user role to find 1 storage service, got %v, err=%v", found, err)
	}

	// User CANNOT register a service (PUT /{id} -> 403 Forbidden)
	err = usrClient.Register(ctx, ServiceRegistration{
		ID:        "2222222222222222222222222222222222222222222222222222222222222222",
		Address:   "http://spoof:4000",
		Protocols: []string{"names-v1"},
		PublicKey: userKey,
	})
	if err == nil {
		t.Fatalf("expected user role to be forbidden from registering a service")
	}

	// User CANNOT create invitations or list tokens
	if _, err := usrClient.CreateInvitation(ctx, "bob", RoleUser, NodeIdentity{}, 5*time.Minute); err == nil {
		t.Fatalf("expected user role to be forbidden from creating invitations")
	}
	if _, err := usrClient.ListAccessTokens(ctx); err == nil {
		t.Fatalf("expected user role to be forbidden from listing access tokens")
	}

	// 9. Coupled revocation: Admin revokes service token -> service's WireGuard key and service entry are immediately evicted
	peerResolver.SetPeer(adminKey, true)
	if err := adminClient.RevokeAccessToken(ctx, svcTokRec.ID, NodeIdentity{}); err != nil {
		t.Fatalf("admin RevokeAccessToken failed: %v", err)
	}

	// Verify serviceKey is no longer authorized
	keyAllowed, _, err := adminClient.VerifyKey(ctx, serviceKey)
	if err != nil || keyAllowed {
		t.Fatalf("expected revoked service's WireGuard key to be evicted, got allowed=%v err=%v", keyAllowed, err)
	}

	// Verify service entry was removed from discovery
	foundAfterRevoke, err := adminClient.Find(ctx, "storage-v1", "", 10)
	if err != nil || len(foundAfterRevoke) != 0 {
		t.Fatalf("expected revoked service to be unregistered, got %v, err=%v", foundAfterRevoke, err)
	}

	// Verify revoked service token can no longer authenticate
	peerResolver.SetPeer(serviceKey, true)
	if _, err := svcClient.Find(ctx, "", "", 10); err == nil {
		t.Fatalf("expected revoked service token to be rejected")
	}
}

func TestRedeemRateLimiting(t *testing.T) {
	d := NewInMemoryDiscovery()
	srv := NewDiscoveryServer(d).WithRequireAuth(true)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body, _ := json.Marshal(map[string]string{
		"code":       "inv_invite_invalidcode",
		"public_key": "nodekey:1111111111111111111111111111111111111111111111111111111111111111",
		"name":       "attacker",
	})

	for i := range 5 {
		resp, err := ts.Client().Post(ts.URL+"/auth/invite/redeem", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("attempt %d failed: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("attempt %d: expected 404, got %d", i, resp.StatusCode)
		}
	}

	// 6th failed attempt within the window must be rate limited (429 Too Many Requests)
	resp, err := ts.Client().Post(ts.URL+"/auth/invite/redeem", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("6th attempt failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests on 6th attempt, got %d", resp.StatusCode)
	}
}

func TestFileSystemDiscoveryInvitationPersistence(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	fsd1, err := NewFileSystemDiscovery(dir, time.Hour)
	if err != nil {
		t.Fatalf("NewFileSystemDiscovery failed: %v", err)
	}

	bootstrapPath := filepath.Join(dir, "bootstrap.invite")
	inv, created, err := EnsureBootstrapInvitation(ctx, fsd1, 15*time.Minute, bootstrapPath)
	if err != nil || !created || inv == nil {
		t.Fatalf("EnsureBootstrapInvitation failed: created=%v err=%v", created, err)
	}
	data, err := os.ReadFile(bootstrapPath)
	if err != nil || string(data) == "" {
		t.Fatalf("expected bootstrap.invite file to be written: %v", err)
	}

	pubKey := "nodekey:9999999999999999999999999999999999999999999999999999999999999999"
	tok, err := fsd1.RedeemInvitation(ctx, inv.Code, pubKey, NodeIdentity{Name: "admin-node"})
	if err != nil {
		t.Fatalf("RedeemInvitation failed: %v", err)
	}
	_ = fsd1.Close()

	// Reopen FileSystemDiscovery and verify state was replayed
	fsd2, err := NewFileSystemDiscovery(dir, time.Hour)
	if err != nil {
		t.Fatalf("reopening NewFileSystemDiscovery failed: %v", err)
	}
	defer fsd2.Close()

	ok, verified, err := fsd2.VerifyAccessToken(ctx, tok.Token)
	if err != nil || !ok || verified.PublicKey != pubKey || verified.Role != RoleAdmin {
		t.Fatalf("expected redeemed token to persist across restart, got ok=%v %+v, err=%v", ok, verified, err)
	}

	// Replay of invitation must still be rejected after restart
	if _, err := fsd2.RedeemInvitation(ctx, inv.Code, pubKey, NodeIdentity{Name: "admin-node"}); !errors.Is(err, ErrInvitationUsed) {
		t.Fatalf("expected ErrInvitationUsed after restart, got %v", err)
	}
}
