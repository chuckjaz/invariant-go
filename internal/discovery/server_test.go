package discovery

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscoveryServer(t *testing.T) {
	discovery := NewInMemoryDiscovery()
	server := NewDiscoveryServer(discovery)
	ts := httptest.NewServer(server)
	defer ts.Close()

	// 1. GET /id
	res, err := http.Get(ts.URL + "/id")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", res.StatusCode)
	}

	// 2. PUT /test-service-id
	reg := ServiceRegistration{
		ID:        "test-service-id",
		Address:   "http://localhost:8080",
		Protocols: []string{"http", "grpc"},
		Tags:      []string{"cache", "source"},
	}

	reqBody, _ := json.Marshal(reg)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/test-service-id", bytes.NewReader(reqBody))
	client := &http.Client{}
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", res.StatusCode)
	}

	// 3. GET /:id
	res, err = http.Get(ts.URL + "/test-service-id")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", res.StatusCode)
	}
	var desc ServiceDescription
	if err := json.NewDecoder(res.Body).Decode(&desc); err != nil {
		t.Fatal(err)
	}
	if desc.ID != reg.ID {
		t.Errorf("expected ID %s, got %s", reg.ID, desc.ID)
	}
	if len(desc.Tags) != 2 || desc.Tags[0] != "cache" || desc.Tags[1] != "source" {
		t.Errorf("expected tags [cache source], got %v", desc.Tags)
	}

	// 4. GET /?protocol=http
	res, err = http.Get(ts.URL + "/?protocol=http")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", res.StatusCode)
	}
	var descs []ServiceDescription
	if err := json.NewDecoder(res.Body).Decode(&descs); err != nil {
		t.Fatal(err)
	}
	if len(descs) != 1 {
		t.Fatalf("expected 1 result, got %d", len(descs))
	}
	if descs[0].ID != reg.ID {
		t.Errorf("expected ID %s, got %s", reg.ID, descs[0].ID)
	}
	if len(descs[0].Tags) != 2 || descs[0].Tags[0] != "cache" || descs[0].Tags[1] != "source" {
		t.Errorf("expected tags [cache source] in find result, got %v", descs[0].Tags)
	}

	// 5. GET /?protocol=unknown
	res, err = http.Get(ts.URL + "/?protocol=unknown")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", res.StatusCode)
	}
	var emptyDescs []ServiceDescription
	if err := json.NewDecoder(res.Body).Decode(&emptyDescs); err != nil {
		t.Fatal(err)
	}
	if len(emptyDescs) != 0 {
		t.Fatalf("expected 0 result, got %d", len(emptyDescs))
	}

	// 6. GET /non-existent-id -> 404
	res, err = http.Get(ts.URL + "/non-existent-id")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 Not Found, got %d", res.StatusCode)
	}

	// 7. PUT /id with invalid JSON -> 400
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/test-service-id", bytes.NewReader([]byte("{invalid json")))
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", res.StatusCode)
	}

	// 8. GET /?protocol=http&count=2
	res, err = http.Get(ts.URL + "/?protocol=http&count=2")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", res.StatusCode)
	}

	// 9. GET /?protocol=http&count=invalid
	res, err = http.Get(ts.URL + "/?protocol=http&count=invalid")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", res.StatusCode)
	}
}
