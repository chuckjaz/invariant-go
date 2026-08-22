package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestNotifyClient_Success(t *testing.T) {
	var receivedStorageID string
	var receivedPayload NotifyRequest
	var receivedMethod string
	var receivedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&receivedPayload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL, nil)

	storageID := "storage-node-1"
	addresses := []string{"addr1", "addr2", "addr3"}

	err := client.Notify(storageID, addresses)
	if err != nil {
		t.Fatalf("Notify failed: %v", err)
	}

	if receivedMethod != http.MethodPut {
		t.Errorf("Expected PUT method, got %s", receivedMethod)
	}
	expectedPath := "/notify/" + storageID
	if receivedPath != expectedPath {
		t.Errorf("Expected path %s, got %s", expectedPath, receivedPath)
	}
	if !slices.Equal(receivedPayload.Addresses, addresses) {
		t.Errorf("Expected payload addresses %v, got %v", addresses, receivedPayload.Addresses)
	}
	_ = receivedStorageID
}

func TestNotifyClient_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(server.URL, server.Client())

	err := client.Notify("node1", []string{"addr1"})
	if err == nil {
		t.Fatal("Expected error on 500 response, got nil")
	}
}

func TestNotifyClient_NetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	serverURL := server.URL
	server.Close() // Close immediately to force network error

	client := NewClient(serverURL, nil)
	err := client.Notify("node1", []string{"addr1"})
	if err == nil {
		t.Fatal("Expected network error on closed server, got nil")
	}
}
