package kv

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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
