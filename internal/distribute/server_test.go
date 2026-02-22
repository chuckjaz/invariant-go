package distribute

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDistributeServer(t *testing.T) {
	d := NewInMemoryDistribute(nil, 3)
	server := NewDistributeServer(d)
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
	hasReq := HasRequest{Addresses: []string{"abc", "def"}}
	body, _ := json.Marshal(hasReq)
	req, err = http.NewRequest(http.MethodPut, ts.URL+"/has/"+testID, bytes.NewBuffer(body))
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
