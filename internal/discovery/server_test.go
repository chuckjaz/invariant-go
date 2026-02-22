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
	ts := httptest.NewServer(server.Handler())
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
}
