package distribute

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"invariant/internal/notify"
)

func TestDistributeServer(t *testing.T) {
	d := NewInMemoryDistribute(nil, 3, 3, "", 0)
	server := NewDistributeServer("", d)
	ts := httptest.NewServer(server)
	defer ts.Close()

	// Generate 32-byte hex random ID
	idBytes := make([]byte, 32)
	rand.Read(idBytes)
	testID := hex.EncodeToString(idBytes)

	// Test GET /id
	resp, err := http.Get(ts.URL + "/id")
	if err != nil {
		t.Fatalf("Failed to GET /id: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %v", resp.StatusCode)
	}

	// Test PUT /register/{id}
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/register/"+testID, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to PUT /register: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200 for register, got %v", resp.StatusCode)
	}

	// Test PUT /has/{id}
	hasReq := notify.NotifyRequest{Addresses: []string{"abc", "def"}}
	body, _ := json.Marshal(hasReq)
	req, err = http.NewRequest(http.MethodPut, ts.URL+"/notify/"+testID, bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to PUT /has: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200 for has, got %v", resp.StatusCode)
	}

	// Verify the blocks were stored
	blocks := d.GetBlocks(testID)
	if len(blocks) != 2 {
		t.Errorf("Expected 2 blocks, got %v", len(blocks))
	}
	hasAbc := false
	hasDef := false
	for _, b := range blocks {
		if b == "abc" {
			hasAbc = true
		}
		if b == "def" {
			hasDef = true
		}
	}
	if !hasAbc || !hasDef {
		t.Errorf("Missing expected blocks, got %v", blocks)
	}
}

type mockErrorDistribute struct{}

func (m *mockErrorDistribute) Register(ctx context.Context, id string) error {
	return errors.New("register error")
}

func (m *mockErrorDistribute) Notify(ctx context.Context, id string, addresses []string) error {
	return errors.New("notify error")
}

func TestDistributeServer_Errors(t *testing.T) {
	// 1. Verify custom server ID & ID() method
	d := NewInMemoryDistribute(nil, 3, 3, "", 0)
	server := NewDistributeServer("custom-server-id", d)
	if server.ID() != "custom-server-id" {
		t.Errorf("Expected ID 'custom-server-id', got %q", server.ID())
	}

	ts := httptest.NewServer(server)
	defer ts.Close()

	// 2. PUT /notify/{id} with invalid JSON -> 400 Bad Request
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/notify/node1", bytes.NewBuffer([]byte("{invalid json")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request, got %d", resp.StatusCode)
	}

	// 3. Test Register and Notify error handling on the server
	errDist := &mockErrorDistribute{}
	errServer := NewDistributeServer("err-srv", errDist)
	errTs := httptest.NewServer(errServer)
	defer errTs.Close()

	// PUT /register/node1 with error backend -> 500
	req, _ = http.NewRequest(http.MethodPut, errTs.URL+"/register/node1", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected 500 Internal Server Error for Register, got %d", resp.StatusCode)
	}

	// PUT /notify/node1 with error backend -> 500
	hasReq := notify.NotifyRequest{Addresses: []string{"abc"}}
	body, _ := json.Marshal(hasReq)
	req, _ = http.NewRequest(http.MethodPut, errTs.URL+"/notify/node1", bytes.NewBuffer(body))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected 500 Internal Server Error for Notify, got %d", resp.StatusCode)
	}
}
