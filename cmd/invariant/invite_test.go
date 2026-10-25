package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chuckjaz/invariant-go/internal/config"
	"github.com/chuckjaz/invariant-go/internal/discovery"
	"github.com/chuckjaz/invariant-go/internal/itp"
	"github.com/chuckjaz/invariant-go/internal/start"
)

func TestCLIInviteRedeemAndTokenResolution(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("INVARIANT_ACCESS_TOKEN", "")
	t.Setenv("INVARIANT_OAUTH_TOKEN", "")

	// Mock OAuth2 UserInfo endpoint
	userInfoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch auth {
		case "Bearer valid-alice-oauth-token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"sub":"123","name":"Alice","email":"alice@example.com"}`)
		case "Bearer valid-bob-oauth-token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"sub":"456","name":"Bob","email":"bob@example.com"}`)
		default:
			http.Error(w, "invalid oauth token", http.StatusUnauthorized)
		}
	}))
	defer userInfoSrv.Close()

	d := discovery.NewInMemoryDiscovery()
	srv := discovery.NewDiscoveryServer(d).
		WithRequireAuth(true).
		WithOAuthAuthenticator(discovery.NewUserInfoAuthenticator(userInfoSrv.URL, userInfoSrv.Client(), time.Minute))
	ts := httptest.NewServer(srv)
	defer ts.Close()
	d.WithControlPlaneURL(ts.URL)
	srv.WithControlPlaneURL(ts.URL)

	ctx := context.Background()
	inv, err := d.CreateInvitation(ctx, "alice", discovery.RoleUser, discovery.NodeIdentity{Name: "admin"}, 15*time.Minute, "alice@example.com")
	if err != nil || inv == nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}

	cfg := &config.InvariantConfig{
		Discovery: ts.URL,
	}

	// 1. Redeeming without logging in first must fail
	_, _, err = redeemInvitationWithOAuth(ctx, cfg, inv.Code, ts.URL, "", oauthLoginOptions{
		userInfoURL: userInfoSrv.URL,
		httpClient:  userInfoSrv.Client(),
	})
	if err == nil {
		t.Fatalf("expected redeemInvitationWithOAuth without prior login to fail")
	}

	// 2. Logging in with invalid OAuth credentials must fail
	_, _, err = performOAuthLogin(ctx, oauthLoginOptions{
		manualToken: "invalid-oauth-token",
		userInfoURL: userInfoSrv.URL,
		httpClient:  userInfoSrv.Client(),
	})
	if err == nil {
		t.Fatalf("expected performOAuthLogin with invalid OAuth token to fail")
	}

	// 3. Logging in as bob@example.com succeeds, but redeeming Alice's invitation must fail due to email mismatch
	_, _, err = performOAuthLogin(ctx, oauthLoginOptions{
		manualToken: "valid-bob-oauth-token",
		userInfoURL: userInfoSrv.URL,
		httpClient:  userInfoSrv.Client(),
	})
	if err != nil {
		t.Fatalf("performOAuthLogin for bob failed: %v", err)
	}
	_, _, err = redeemInvitationWithOAuth(ctx, cfg, inv.Code, ts.URL, "", oauthLoginOptions{
		userInfoURL: userInfoSrv.URL,
		httpClient:  userInfoSrv.Client(),
	})
	if err == nil {
		t.Fatalf("expected redeemInvitationWithOAuth with mismatched OAuth email to fail")
	}

	// 4. Logging in as alice@example.com and then redeeming the invitation via saved OAuth credentials must succeed
	_, _, err = performOAuthLogin(ctx, oauthLoginOptions{
		manualToken: "valid-alice-oauth-token",
		userInfoURL: userInfoSrv.URL,
		httpClient:  userInfoSrv.Client(),
	})
	if err != nil {
		t.Fatalf("performOAuthLogin for alice failed: %v", err)
	}
	rec, _, err := redeemInvitationWithOAuth(ctx, cfg, inv.Code, ts.URL, "", oauthLoginOptions{
		userInfoURL: userInfoSrv.URL,
		httpClient:  userInfoSrv.Client(),
	})
	if err != nil {
		t.Fatalf("redeemInvitationWithOAuth with matching OAuth email failed: %v", err)
	}
	if rec.Email != "alice@example.com" {
		t.Fatalf("expected redeemed token email 'alice@example.com', got %q", rec.Email)
	}

	resolved := resolveUserToken(cfg)
	if resolved != rec.Token {
		t.Fatalf("expected resolveUserToken to return %q, got %q", rec.Token, resolved)
	}

	// Verify ~/.invariant/oauth_token.json retains the OAuth token and verified email
	savedOAuth, err := LoadOAuthTokenFile()
	if err != nil {
		t.Fatalf("LoadOAuthTokenFile failed: %v", err)
	}
	if savedOAuth.AccessToken != "valid-alice-oauth-token" || savedOAuth.Email != "alice@example.com" {
		t.Fatalf("unexpected saved OAuthTokenFile: %+v", savedOAuth)
	}
}

func TestProvisionHelperProcess(t *testing.T) {
	svcType := os.Getenv("GO_PROVISION_HELPER_SVC")
	if svcType == "" {
		return
	}

	// Find args after "--"
	var args []string
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}

	fs := flag.NewFlagSet(svcType, flag.ExitOnError)
	var dir, discoveryURL, provisionInvite, inviteCode, tokenFile, keyFile string
	var port int
	var listenIP bool
	fs.StringVar(&dir, "dir", "", "")
	fs.StringVar(&discoveryURL, "discovery", "", "")
	fs.StringVar(&provisionInvite, "provision", "", "")
	fs.StringVar(&inviteCode, "invite", "", "")
	fs.StringVar(&tokenFile, "token-file", "", "")
	fs.StringVar(&keyFile, "key-file", "", "")
	fs.IntVar(&port, "port", 3003, "")
	fs.BoolVar(&listenIP, "listen-ip", false, "")
	_ = fs.Parse(args)

	if svcType == "inv-discovery" {
		fsd, err := discovery.NewFileSystemDiscovery(dir, time.Hour)
		if err != nil {
			os.Exit(1)
		}
		defer fsd.Close()

		cpURL := fmt.Sprintf("http://127.0.0.1:%d", port)
		fsd.WithControlPlaneURL(cpURL)
		srv := discovery.NewDiscoveryServer(fsd).
			WithControlPlane(fsd).
			WithServiceKeyManager(fsd).
			WithInvitationManager(fsd).
			WithControlPlaneURL(cpURL).
			WithRequireAuth(true)

		invitePath := filepath.Join(dir, "bootstrap.invite")
		_, _, _ = discovery.EnsureBootstrapInvitation(context.Background(), fsd, time.Hour, invitePath, cpURL)

		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			os.Exit(1)
		}
		fmt.Printf("Discovery service listening on :%d (HTTP)...\n", port)
		_ = http.Serve(ln, srv)
		os.Exit(0)
	}

	// Service helper (inv-names / inv-storage)
	if provisionInvite != "" {
		inviteCode = provisionInvite
	}
	keyFile = itp.ServiceKeyPath(dir, svcType, keyFile)
	privKey, err := itp.LoadOrGenerateKey(keyFile)
	if err != nil {
		os.Exit(1)
	}
	_, err = itp.ResolveOrRedeemToken(context.Background(), itp.ServiceAuthOptions{
		DiscoveryURL: discoveryURL,
		Dir:          dir,
		ServiceID:    svcType,
		ServiceName:  svcType,
		InviteCode:   inviteCode,
		TokenFile:    tokenFile,
		PrivateKey:   privKey,
	})
	if err != nil {
		os.Exit(1)
	}
	if provisionInvite != "" {
		os.Exit(0)
	}
	os.Exit(0)
}

func TestProvisionServicesCommand(t *testing.T) {
	testExe, err := os.Executable()
	if err != nil {
		t.Fatalf("failed to get test executable: %v", err)
	}

	tmpDir := t.TempDir()
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("failed to create binDir: %v", err)
	}

	for _, svc := range []string{"inv-discovery", "inv-names", "inv-storage"} {
		script := fmt.Sprintf("#!/bin/sh\nexport GO_PROVISION_HELPER_SVC=%s\nexec %q -test.run=TestProvisionHelperProcess -- \"$@\"\n", svc, testExe)
		scriptPath := filepath.Join(binDir, svc)
		if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
			t.Fatalf("failed to write helper script %s: %v", svc, err)
		}
	}

	t.Setenv("HOME", tmpDir)
	t.Setenv("INVARIANT_ACCESS_TOKEN", "")
	t.Setenv("INVARIANT_OAUTH_TOKEN", "")
	t.Setenv("INVARIANT_SERVICE_KEY", "")

	// Pick a free local port for discovery in listen-ip mode
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	yamlContent := fmt.Sprintf(`
services:
  - command: inv-discovery
    args:
      dir: "*/data/discovery"
      port: "%d"
      listen-ip: "true"

  - command: inv-names
    args:
      dir: "*/data/names"

  - command: inv-storage
    args:
      dir: "*/data/storage"
`, port)

	configPath := filepath.Join(tmpDir, "services.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write services.yaml: %v", err)
	}

	svcCfg, err := start.LoadConfig(configPath, "")
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	globalCfg := &config.InvariantConfig{}
	if err := provisionServices(ctx, globalCfg, svcCfg, binDir, "", false); err != nil {
		t.Fatalf("provisionServices failed: %v", err)
	}

	// Verify service.token and discovery.url files exist in each service's configuration directory
	expectedDiscURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	for _, svcDir := range []string{"names", "storage"} {
		tokPath := filepath.Join(tmpDir, "data", svcDir, "service.token")
		tok, err := itp.LoadAccessToken(tokPath)
		if err != nil || tok == "" {
			t.Fatalf("expected provisioned service token at %s, got err=%v", tokPath, err)
		}
		discURLPath := filepath.Join(tmpDir, "data", svcDir, "discovery.url")
		discURL, err := itp.LoadAccessToken(discURLPath)
		if err != nil || discURL != expectedDiscURL {
			t.Fatalf("expected persisted discovery.url %q at %s, got %q (err=%v)", expectedDiscURL, discURLPath, discURL, err)
		}
	}

	// Running provisionServices a second time should validate existing tokens and succeed idempotently
	if err := provisionServices(ctx, globalCfg, svcCfg, binDir, "", false); err != nil {
		t.Fatalf("second provisionServices run failed: %v", err)
	}
}

func TestResolveOrInstallCommand(t *testing.T) {
	tmpDir := t.TempDir()
	binDir := filepath.Join(tmpDir, "bin")
	goBinDir := filepath.Join(tmpDir, "go", "bin")
	fakePathDir := filepath.Join(tmpDir, "path")
	for _, d := range []string{binDir, goBinDir, fakePathDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	t.Setenv("HOME", tmpDir)
	t.Setenv("GOBIN", goBinDir)
	t.Setenv("PATH", fakePathDir+string(os.PathListSeparator)+goBinDir)

	// Without --install, a missing command should return an error
	if _, err := resolveOrInstallCommand(context.Background(), "inv-storage", binDir, false); err == nil {
		t.Fatalf("expected error when command is missing and install=false")
	}

	// Create a fake 'go' binary in fakePathDir that writes the target binary into GOBIN on 'go install'
	fakeGo := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "install" ] && [ "$2" = "github.com/chuckjaz/invariant-go/cmd/inv-storage@latest" ]; then
  printf '#!/bin/sh\nexit 0\n' > %q/inv-storage
  chmod 0755 %q/inv-storage
  exit 0
fi
exit 1
`, goBinDir, goBinDir)
	if err := os.WriteFile(filepath.Join(fakePathDir, "go"), []byte(fakeGo), 0755); err != nil {
		t.Fatalf("failed to write fake go binary: %v", err)
	}

	resolved, err := resolveOrInstallCommand(context.Background(), "inv-storage", binDir, true)
	if err != nil {
		t.Fatalf("resolveOrInstallCommand with installMissing=true failed: %v", err)
	}
	expectedPath := filepath.Join(goBinDir, "inv-storage")
	if resolved != expectedPath {
		t.Fatalf("expected resolved path %q, got %q", expectedPath, resolved)
	}
}
