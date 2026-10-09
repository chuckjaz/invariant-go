package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"invariant/internal/config"
	"invariant/internal/discovery"
)

func TestServiceKeyCLI_CreateListRevoke(t *testing.T) {
	// Set up temporary home directory for ~/.invariant
	tempHome, err := os.MkdirTemp("", "invariant-cli-test-*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tempHome)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", origHome)

	// Set up mock discovery server with OAuth authenticator
	userToken := "test-oauth-user-token"
	userIdentity := discovery.NodeIdentity{Name: "Alice User", Email: "alice@example.com"}

	d := discovery.NewInMemoryDiscovery()
	server := discovery.NewDiscoveryServer(d)
	server.WithOAuthAuthenticator(discovery.NewStaticAuthenticator(map[string]discovery.NodeIdentity{
		userToken: userIdentity,
	}))
	server.WithRequireAuth(true)

	ts := httptest.NewServer(server)
	defer ts.Close()

	// Save OAuth token
	tokenFile := &OAuthTokenFile{
		AccessToken: userToken,
		TokenType:   "Bearer",
		Name:        "Alice User",
		Email:       "alice@example.com",
	}
	if err := SaveOAuthToken(tokenFile); err != nil {
		t.Fatalf("failed to save oauth token: %v", err)
	}

	loadedToken, err := LoadOAuthToken()
	if err != nil || loadedToken != userToken {
		t.Fatalf("failed to load oauth token: %v, loaded=%s", err, loadedToken)
	}

	cfg := &config.InvariantConfig{
		Discovery: ts.URL,
	}

	// 1. Create service key
	client, _ := getAuthenticatedClient(cfg)
	ctx := context.Background()

	rec, err := client.CreateServiceKey(ctx, "worker-node-1", userIdentity, nil)
	if err != nil {
		t.Fatalf("failed to create service key: %v", err)
	}
	if rec.ID == "" || rec.Key == "" {
		t.Fatalf("created key missing ID or secret: %+v", rec)
	}
	if rec.CreatedBy.Email != "alice@example.com" {
		t.Fatalf("expected creator alice@example.com, got %+v", rec.CreatedBy)
	}

	// 2. List service keys
	keys, err := client.ListServiceKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("expected 1 service key in list, got %d, err=%v", len(keys), err)
	}

	// 3. Verify that a microservice can connect using this service key
	svcClient := discovery.NewClient(ts.URL, ts.Client()).WithServiceKey(rec.Key)
	err = svcClient.Register(ctx, discovery.ServiceRegistration{
		ID:        "storage-test",
		Address:   "http://127.0.0.1:4000",
		Protocols: []string{"storage-v1"},
	})
	if err != nil {
		t.Fatalf("microservice failed to register using service key: %v", err)
	}

	// 4. Revoke service key
	err = client.RevokeServiceKey(ctx, rec.ID, userIdentity)
	if err != nil {
		t.Fatalf("failed to revoke service key: %v", err)
	}

	// 5. Revoked key can no longer register
	err = svcClient.Register(ctx, discovery.ServiceRegistration{
		ID:        "storage-test-2",
		Address:   "http://127.0.0.1:4001",
		Protocols: []string{"storage-v1"},
	})
	if err == nil {
		t.Fatalf("expected registration with revoked key to fail, got success")
	}
}

func TestResolveUserToken(t *testing.T) {
	tempHome, err := os.MkdirTemp("", "token-res-test-*")
	if err != nil {
		t.Fatalf("failed to create temp home: %v", err)
	}
	defer os.RemoveAll(tempHome)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", origHome)

	// 1. Initially empty
	cfg := &config.InvariantConfig{}
	if tok := resolveUserToken(cfg); tok != "" {
		t.Fatalf("expected empty token, got %q", tok)
	}

	// 2. From config file
	cfg.OAuthToken = "config-oauth-token"
	if tok := resolveUserToken(cfg); tok != "config-oauth-token" {
		t.Fatalf("expected config-oauth-token, got %q", tok)
	}
	cfg.OAuthToken = ""

	// 3. From env var
	os.Setenv("INVARIANT_OAUTH_TOKEN", "env-oauth-token")
	if tok := resolveUserToken(cfg); tok != "env-oauth-token" {
		t.Fatalf("expected env-oauth-token, got %q", tok)
	}
	os.Unsetenv("INVARIANT_OAUTH_TOKEN")

	// 4. From oauth_token.json file
	invDir := filepath.Join(tempHome, ".invariant")
	os.MkdirAll(invDir, 0755)
	os.WriteFile(filepath.Join(invDir, "oauth_token.json"), []byte(`{"access_token": "file-oauth-token"}`), 0600)

	if tok := resolveUserToken(cfg); tok != "file-oauth-token" {
		t.Fatalf("expected file-oauth-token, got %q", tok)
	}
}
