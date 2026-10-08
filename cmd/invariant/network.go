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

func newHTTPClient(cfg *config.InvariantConfig) *http.Client {
	pk := getClientKey()
	authToken := ""
	derpMapURL := ""
	if cfg != nil {
		authToken = cfg.AuthToken
		derpMapURL = cfg.DERPMapURL
	}
	if authToken == "" {
		authToken = os.Getenv("INVARIANT_AUTH_TOKEN")
	}

	return itp.NewHTTPClient(pk.Private, authToken, itp.WithDERPMapURL(derpMapURL))
}

func newDiscoveryClient(discoveryURL string, cfg *config.InvariantConfig) *discovery.Client {
	httpClient := newHTTPClient(cfg)
	client := discovery.NewClient(discoveryURL, httpClient)
	authToken := ""
	if cfg != nil {
		authToken = cfg.AuthToken
	}
	if authToken == "" {
		authToken = os.Getenv("INVARIANT_AUTH_TOKEN")
	}
	if authToken != "" {
		client = client.WithAuthToken(authToken)
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
