package identity

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIdentityClient_Success(t *testing.T) {
	expectedID := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/id" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(expectedID))
	}))
	defer server.Close()

	// 1. With http:// in baseURL and nil httpClient
	client := NewClient(server.URL, nil)
	if id := client.ID(); id != expectedID {
		t.Errorf("Expected ID %s, got %s", expectedID, id)
	}

	// 2. Without http:// prefix
	rawHost := strings.TrimPrefix(server.URL, "http://")
	clientNoPrefix := NewClient(rawHost, server.Client())
	if id := clientNoPrefix.ID(); id != expectedID {
		t.Errorf("Expected ID %s, got %s", expectedID, id)
	}
}

func TestIdentityClient_Errors(t *testing.T) {
	// 1. Server returning 500 error
	serverErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer serverErr.Close()

	clientErr := NewClient(serverErr.URL, nil)
	if id := clientErr.ID(); id != "" {
		t.Errorf("Expected empty string on 500 error, got %s", id)
	}

	// 2. Closed server / Network error
	serverClosed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	closedURL := serverClosed.URL
	serverClosed.Close()

	clientClosed := NewClient(closedURL, nil)
	if id := clientClosed.ID(); id != "" {
		t.Errorf("Expected empty string on closed server, got %s", id)
	}
}
