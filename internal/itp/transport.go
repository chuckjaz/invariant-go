package itp

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

type clientEntry struct {
	client    *tailcat.Client
	connected bool
}

var initDialMu sync.Mutex

// Transport is an http.RoundTripper that handles itp:// URLs
// using Tailcat WireGuard connections, while delegating http:// and https:// URLs.
type Transport struct {
	Base         http.RoundTripper
	ClientKey    key.NodePrivate
	BearerToken  string
	DERPMapURL   string
	itpTransport *http.Transport

	mu      sync.Mutex
	clients map[tailcat.Addr]*clientEntry
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
func NewTransport(clientKey key.NodePrivate, bearerToken string, opts ...TransportOption) *Transport {
	t := &Transport{
		ClientKey:   clientKey,
		BearerToken: bearerToken,
		clients:     make(map[tailcat.Addr]*clientEntry),
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
			entry := t.getOrCreateClient(tailcat.Addr(host))
			t.mu.Lock()
			connected := entry.connected
			t.mu.Unlock()

			// Tailcat creates a separate DERP connection per Client instance.
			// When multiple Clients share the same ClientKey and perform their
			// initial DERP meow/meowed handshake concurrently, the DERP server
			// only routes replies to the most recent writer for that public key,
			// causing concurrent handshakes to drop packets and wait for the 1s
			// retry ticker. Serializing the initial dial per peer avoids DERP
			// active-client contention while allowing subsequent dials to run in parallel.
			if !connected {
				initDialMu.Lock()
				t.mu.Lock()
				connected = entry.connected
				t.mu.Unlock()
				if !connected {
					conn, err := entry.client.DialTCPPort(ctx, uint16(port))
					if err == nil {
						t.mu.Lock()
						entry.connected = true
						t.mu.Unlock()
					}
					initDialMu.Unlock()
					return conn, err
				}
				initDialMu.Unlock()
			}
			return entry.client.DialTCPPort(ctx, uint16(port))
		},
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return t
}

func (t *Transport) getOrCreateClient(tcAddr tailcat.Addr) *clientEntry {
	t.mu.Lock()
	defer t.mu.Unlock()

	if entry, ok := t.clients[tcAddr]; ok {
		return entry
	}

	logf := logger.Logf(log.Printf)
	if log.Writer() == io.Discard {
		logf = logger.Discard
	}

	entry := &clientEntry{
		client: &tailcat.Client{
			Server:     tcAddr,
			Key:        t.ClientKey,
			Logf:       logf,
			DERPMapURL: t.DERPMapURL,
		},
	}
	t.clients[tcAddr] = entry
	return entry
}

// RoundTrip executes a single HTTP transaction.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	scheme := strings.ToLower(req.URL.Scheme)
	if scheme == "itp" {
		reqClone := req.Clone(req.Context())
		reqClone.URL.Scheme = "http"

		// Ensure host has port for DialContext
		if !strings.Contains(reqClone.URL.Host, ":") {
			reqClone.URL.Host = net.JoinHostPort(reqClone.URL.Host, "80")
		}

		if t.BearerToken != "" && reqClone.Header.Get("Authorization") == "" {
			reqClone.Header.Set("Authorization", "Bearer "+t.BearerToken)
		}

		return t.itpTransport.RoundTrip(reqClone)
	}

	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	if t.BearerToken != "" && req.Header.Get("Authorization") == "" {
		reqClone := req.Clone(req.Context())
		reqClone.Header.Set("Authorization", "Bearer "+t.BearerToken)
		return base.RoundTrip(reqClone)
	}

	return base.RoundTrip(req)
}

// Close closes all active Tailcat client connections managed by this transport.
func (t *Transport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	var firstErr error
	for _, entry := range t.clients {
		if err := entry.client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	t.clients = make(map[tailcat.Addr]*clientEntry)
	t.itpTransport.CloseIdleConnections()
	return firstErr
}

// NewHTTPClient returns an *http.Client configured with an ITP Transport.
func NewHTTPClient(clientKey key.NodePrivate, bearerToken string, opts ...TransportOption) *http.Client {
	return &http.Client{
		Transport: NewTransport(clientKey, bearerToken, opts...),
	}
}
