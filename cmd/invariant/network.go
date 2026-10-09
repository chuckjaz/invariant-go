package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/tailscale/tailcat"
	"invariant/internal/config"
	"invariant/internal/discovery"
	"invariant/internal/itp"
)

var (
	cachedClientKey *tailcat.PrivateKey
	cachedKeyOnce   sync.Once
	registeredCPs   = make(map[string]bool)
	registeredCPsMu sync.Mutex
)

func getClientKey() *tailcat.PrivateKey {
	cachedKeyOnce.Do(func() {
		keyFile := itp.DefaultKeyPath("client")
		pk, err := itp.LoadOrGenerateKey(keyFile)
		if err != nil {
			log.Fatalf("Failed to load or generate client key: %v", err)
		}
		cachedClientKey = pk
	})
	return cachedClientKey
}

func resolveUserToken(cfg *config.InvariantConfig) string {
	if tok, err := LoadOAuthToken(); err == nil && tok != "" {
		return tok
	}
	if tok := os.Getenv("INVARIANT_OAUTH_TOKEN"); tok != "" {
		return tok
	}
	if cfg != nil && cfg.OAuthToken != "" {
		return cfg.OAuthToken
	}
	if tok := os.Getenv("INVARIANT_SERVICE_KEY"); tok != "" {
		return tok
	}
	if cfg != nil && cfg.ServiceKey != "" {
		return cfg.ServiceKey
	}
	return ""
}

func newHTTPClient(cfg *config.InvariantConfig) *http.Client {
	pk := getClientKey()
	token := resolveUserToken(cfg)
	derpMapURL := ""
	if cfg != nil {
		derpMapURL = cfg.DERPMapURL
	}

	return itp.NewHTTPClient(pk.Private, token, itp.WithDERPMapURL(derpMapURL))
}

func newDiscoveryClient(discoveryURL string, cfg *config.InvariantConfig) *discovery.Client {
	httpClient := newHTTPClient(cfg)
	client := discovery.NewClient(discoveryURL, httpClient)
	token := resolveUserToken(cfg)
	if token != "" {
		client = client.WithUserToken(token)
	}

	// Register client WireGuard key with Discovery control plane so other services admit this client
	registeredCPsMu.Lock()
	alreadyRegistered := registeredCPs[discoveryURL]
	if !alreadyRegistered {
		registeredCPs[discoveryURL] = true
	}
	registeredCPsMu.Unlock()

	if !alreadyRegistered {
		pk := getClientKey()
		userName := os.Getenv("USER")
		if userName == "" {
			userName = "cli"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.RegisterClient(ctx, pk.Private.Public().String(), discovery.NodeIdentity{
			Name: userName,
		})
	}

	return client
}
