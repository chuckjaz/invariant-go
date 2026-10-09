package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestUserInfoAuthenticator(t *testing.T) {
	var requestCount atomic.Int32

	userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		token := r.Header.Get("Authorization")
		if token != "Bearer valid-oauth-token" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"sub":            "12345678",
			"name":           "Carol Danvers",
			"email":          "carol@example.com",
			"email_verified": true,
		})
	}))
	defer userServer.Close()

	auth := NewUserInfoAuthenticator(userServer.URL, userServer.Client(), 100*time.Millisecond)
	ctx := context.Background()

	// 1. Valid token
	ident, err := auth.Authenticate(ctx, "valid-oauth-token")
	if err != nil {
		t.Fatalf("expected successful auth: %v", err)
	}
	if ident.Name != "Carol Danvers" || ident.Email != "carol@example.com" {
		t.Fatalf("unexpected identity: %+v", ident)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("expected 1 request, got %d", requestCount.Load())
	}

	// 2. Second request should hit cache
	ident2, err := auth.Authenticate(ctx, "valid-oauth-token")
	if err != nil {
		t.Fatalf("expected successful cached auth: %v", err)
	}
	if ident2.Name != "Carol Danvers" {
		t.Fatalf("unexpected cached identity: %+v", ident2)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("expected cache hit (still 1 request), got %d", requestCount.Load())
	}

	// 3. Invalid token
	_, err = auth.Authenticate(ctx, "bad-token")
	if err == nil {
		t.Fatalf("expected error for bad token, got nil")
	}

	// 4. Wait for cache TTL expiration
	time.Sleep(150 * time.Millisecond)
	_, err = auth.Authenticate(ctx, "valid-oauth-token")
	if err != nil {
		t.Fatalf("expected successful re-auth after TTL: %v", err)
	}
	if requestCount.Load() != 3 {
		t.Fatalf("expected 3 total requests (2 valid, 1 bad), got %d", requestCount.Load())
	}
}
