package itp

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
)

// Transport is an http.RoundTripper that handles itp:// and tailcat:// URLs
// using Tailcat WireGuard connections, while delegating http:// and https:// URLs.
type Transport struct {
	Base         http.RoundTripper
	ClientKey    key.NodePrivate
	AuthToken    string
	DERPMapURL   string
	itpTransport *http.Transport

	mu      sync.Mutex
	clients map[tailcat.Addr]*tailcat.Client
}

// TransportOption configures a Transport.
type TransportOption func(*Transport)

// WithDERPMapURL sets the DERP map URL for the transport.
func WithDERPMapURL(url string) TransportOption {
	return func(t *Transport) {
		t.DERPMapURL = url
	}
}

// NewTransport creates an ITP-aware HTTP transport.
func NewTransport(clientKey key.NodePrivate, authToken string, opts ...TransportOption) *Transport {
	t := &Transport{
		ClientKey: clientKey,
		AuthToken: authToken,
		clients:   make(map[tailcat.Addr]*tailcat.Client),
	}
	for _, opt := range opts {
		opt(t)
	}

	t.itpTransport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, portStr, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
				portStr = "80"
			}
			port := 80
			if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
				port = p
			}
			client := t.getOrCreateClient(tailcat.Addr(host))
			return client.DialTCPPort(ctx, uint16(port))
		},
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return t
}

func (t *Transport) getOrCreateClient(tcAddr tailcat.Addr) *tailcat.Client {
	t.mu.Lock()
	defer t.mu.Unlock()

	if c, ok := t.clients[tcAddr]; ok {
		return c
	}

	c := &tailcat.Client{
		Server:     tcAddr,
		Key:        t.ClientKey,
		DERPMapURL: t.DERPMapURL,
	}
	t.clients[tcAddr] = c
	return c
}

// RoundTrip executes a single HTTP transaction.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	scheme := strings.ToLower(req.URL.Scheme)
	if scheme == "itp" || scheme == "tailcat" {
		reqClone := req.Clone(req.Context())
		reqClone.URL.Scheme = "http"

		// Ensure host has port for DialContext
		if !strings.Contains(reqClone.URL.Host, ":") {
			reqClone.URL.Host = net.JoinHostPort(reqClone.URL.Host, "80")
		}

		if t.AuthToken != "" && reqClone.Header.Get("Authorization") == "" {
			reqClone.Header.Set("Authorization", "Bearer "+t.AuthToken)
		}

		return t.itpTransport.RoundTrip(reqClone)
	}

	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	if t.AuthToken != "" && req.Header.Get("Authorization") == "" {
		reqClone := req.Clone(req.Context())
		reqClone.Header.Set("Authorization", "Bearer "+t.AuthToken)
		return base.RoundTrip(reqClone)
	}

	return base.RoundTrip(req)
}

// Close closes all active Tailcat client connections managed by this transport.
func (t *Transport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	var firstErr error
	for _, c := range t.clients {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	t.clients = make(map[tailcat.Addr]*tailcat.Client)
	t.itpTransport.CloseIdleConnections()
	return firstErr
}

// NewHTTPClient returns an *http.Client configured with an ITP Transport.
func NewHTTPClient(clientKey key.NodePrivate, authToken string, opts ...TransportOption) *http.Client {
	return &http.Client{
		Transport: NewTransport(clientKey, authToken, opts...),
	}
}
