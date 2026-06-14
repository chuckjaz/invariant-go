package kv

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"tailscale.com/client/tailscale/apitype"
)

type mockTailscaleClient struct {
	whoIsFunc func(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error)
}

func (m *mockTailscaleClient) WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error) {
	if m.whoIsFunc != nil {
		return m.whoIsFunc(ctx, remoteAddr)
	}
	return nil, fmt.Errorf("not implemented")
}

func TestServerWhoIs(t *testing.T) {
	expectedResponse := &apitype.WhoIsResponse{}

	mockTS := &mockTailscaleClient{
		whoIsFunc: func(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error) {
			return expectedResponse, nil
		},
	}

	server := NewServer(nil)
	defer server.Close()
	server.tsClient = mockTS

	var receivedWhoIs *apitype.WhoIsResponse
	var receivedOk bool

	server.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedWhoIs, receivedOk = WhoIsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/get?key=foo", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if !receivedOk {
		t.Errorf("Expected WhoIs to be present in context")
	}
	if receivedWhoIs != expectedResponse {
		t.Errorf("Expected WhoIs response %v, got %v", expectedResponse, receivedWhoIs)
	}
}

func TestServerWhoIs_Error(t *testing.T) {
	mockTS := &mockTailscaleClient{
		whoIsFunc: func(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error) {
			return nil, fmt.Errorf("not in tailnet")
		},
	}

	server := NewServer(nil)
	defer server.Close()
	server.tsClient = mockTS

	var receivedOk bool
	server.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, receivedOk = WhoIsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/get?key=foo", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if receivedOk {
		t.Errorf("Expected WhoIs to NOT be present in context when WhoIs returns error")
	}
}

func TestServerWhoIs_Cached(t *testing.T) {
	expectedResponse1 := &apitype.WhoIsResponse{}
	expectedResponse2 := &apitype.WhoIsResponse{}

	callCount := 0
	mockTS := &mockTailscaleClient{
		whoIsFunc: func(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error) {
			callCount++
			if callCount == 1 {
				return expectedResponse1, nil
			}
			return expectedResponse2, nil
		},
	}

	server := NewServer(nil)
	defer server.Close()
	server.tsClient = mockTS
	server.WhoIsTTL = 100 * time.Millisecond

	var receivedWhoIs *apitype.WhoIsResponse
	server.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedWhoIs, _ = WhoIsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	// First call (cache miss)
	req1 := httptest.NewRequest("GET", "/get?key=foo", nil)
	req1.RemoteAddr = "100.0.0.1:1234"
	w1 := httptest.NewRecorder()
	server.ServeHTTP(w1, req1)

	if receivedWhoIs != expectedResponse1 {
		t.Errorf("Expected first WhoIs response %v, got %v", expectedResponse1, receivedWhoIs)
	}
	if callCount != 1 {
		t.Errorf("Expected 1 call to WhoIs, got %d", callCount)
	}

	// Second call immediately from same RemoteAddr (cache hit)
	req2 := httptest.NewRequest("GET", "/get?key=foo", nil)
	req2.RemoteAddr = "100.0.0.1:1234"
	w2 := httptest.NewRecorder()
	server.ServeHTTP(w2, req2)

	if receivedWhoIs != expectedResponse1 {
		t.Errorf("Expected cached response %v, got %v", expectedResponse1, receivedWhoIs)
	}
	if callCount != 1 {
		t.Errorf("Expected callCount to remain 1 due to caching, got %d", callCount)
	}

	// Wait for TTL to expire
	time.Sleep(150 * time.Millisecond)

	// Third call after expiration (cache miss)
	req3 := httptest.NewRequest("GET", "/get?key=foo", nil)
	req3.RemoteAddr = "100.0.0.1:1234"
	w3 := httptest.NewRecorder()
	server.ServeHTTP(w3, req3)

	if receivedWhoIs != expectedResponse2 {
		t.Errorf("Expected new response %v after cache expiration, got %v", expectedResponse2, receivedWhoIs)
	}
	if callCount != 2 {
		t.Errorf("Expected 2 calls to WhoIs after expiration, got %d", callCount)
	}
}

func TestServerWhoIs_BackgroundCleanup(t *testing.T) {
	mockTS := &mockTailscaleClient{
		whoIsFunc: func(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error) {
			return &apitype.WhoIsResponse{}, nil
		},
	}

	server := NewServer(nil)
	defer server.Close()
	server.tsClient = mockTS
	server.WhoIsTTL = 100 * time.Millisecond

	// Create request to populate the cache
	req := httptest.NewRequest("GET", "/get?key=foo", nil)
	req.RemoteAddr = "100.0.0.1:1234"
	w := httptest.NewRecorder()

	server.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server.ServeHTTP(w, req)

	// Verify it's in the cache
	_, _, ok := server.cache.Get("100.0.0.1:1234", 100*time.Millisecond)
	if !ok {
		t.Fatalf("Expected entry to be in cache")
	}

	// Wait for the background cleanup loop to run (interval is TTL / 2 = 50ms)
	var found bool
	for start := time.Now(); time.Since(start) < 500*time.Millisecond; {
		server.cache.mu.RLock()
		_, found = server.cache.entries["100.0.0.1:1234"]
		server.cache.mu.RUnlock()
		if !found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if found {
		t.Errorf("Expected entry to be pruned from the cache by background goroutine, but it was found")
	}
}

func TestServerIdentity(t *testing.T) {
	server := NewServer(nil)
	defer server.Close()

	req := httptest.NewRequest("GET", "/id", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status OK, got %v", w.Code)
	}
	body := w.Body.String()
	if body != server.ID() {
		t.Errorf("Expected ID %q, got %q", server.ID(), body)
	}
	if len(body) != 64 {
		t.Errorf("Expected 64-char hex ID, got len %d", len(body))
	}
}
