package main

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/chuckjaz/invariant-go/internal/config"
	"github.com/chuckjaz/invariant-go/internal/discovery"
	"github.com/chuckjaz/invariant-go/internal/itp"
)

func TestCLIInviteRedeemAndTokenResolution(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("INVARIANT_ACCESS_TOKEN", "")
	t.Setenv("INVARIANT_OAUTH_TOKEN", "")

	d := discovery.NewInMemoryDiscovery()
	srv := discovery.NewDiscoveryServer(d).WithRequireAuth(true)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx := context.Background()
	bootstrapPath := filepath.Join(tmpHome, "bootstrap.invite")
	inv, _, err := discovery.EnsureBootstrapInvitation(ctx, d, 15*time.Minute, bootstrapPath)
	if err != nil || inv == nil {
		t.Fatalf("EnsureBootstrapInvitation failed: %v", err)
	}

	cfg := &config.InvariantConfig{
		Discovery: ts.URL,
	}

	// Redeem bootstrap invitation via CLI helper using local HTTP client
	pk := getClientKey()
	unauthClient := discovery.NewClient(ts.URL, ts.Client())
	rec, err := unauthClient.RedeemInvitation(ctx, inv.Code, pk.Private.Public().String(), discovery.NodeIdentity{Name: "cli-test"})
	if err != nil {
		t.Fatalf("RedeemInvitation failed: %v", err)
	}

	tokenPath := itp.DefaultTokenPath("client")
	if err := itp.SaveAccessToken(tokenPath, rec.Token); err != nil {
		t.Fatalf("SaveAccessToken failed: %v", err)
	}

	resolved := resolveUserToken(cfg)
	if resolved != rec.Token {
		t.Fatalf("expected resolveUserToken to return %q, got %q", rec.Token, resolved)
	}
}
