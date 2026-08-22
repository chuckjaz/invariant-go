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

	// 5. GET /?tag=cache
	res, err = http.Get(ts.URL + "/?tag=cache")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", res.StatusCode)
	}
	var tagDescs []ServiceDescription
	if err := json.NewDecoder(res.Body).Decode(&tagDescs); err != nil {
		t.Fatal(err)
	}
	if len(tagDescs) != 1 || tagDescs[0].ID != reg.ID {
		t.Errorf("expected 1 result with ID %s, got %v", reg.ID, tagDescs)
	}

	// 6. GET /?protocol=http&tag=cache
	res, err = http.Get(ts.URL + "/?protocol=http&tag=cache")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", res.StatusCode)
	}
	var comboDescs []ServiceDescription
	if err := json.NewDecoder(res.Body).Decode(&comboDescs); err != nil {
		t.Fatal(err)
	}
	if len(comboDescs) != 1 || comboDescs[0].ID != reg.ID {
		t.Errorf("expected 1 result for protocol+tag combo, got %v", comboDescs)
	}

	// 7. GET /?protocol=http&tag=mismatched
	res, err = http.Get(ts.URL + "/?protocol=http&tag=mismatched")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", res.StatusCode)
	}
	var mismatchedDescs []ServiceDescription
	if err := json.NewDecoder(res.Body).Decode(&mismatchedDescs); err != nil {
		t.Fatal(err)
	}
	if len(mismatchedDescs) != 0 {
		t.Errorf("expected 0 results for mismatched tag, got %d", len(mismatchedDescs))
	}

	// 8. GET /?protocol=unknown
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

	// 10. Register second service and query with count=0 (unlimited)
	reg2 := ServiceRegistration{
		ID:        "test-service-id-2",
		Address:   "http://localhost:8081",
		Protocols: []string{"http"},
	}
	reqBody2, _ := json.Marshal(reg2)
	req2, _ := http.NewRequest(http.MethodPut, ts.URL+"/test-service-id-2", bytes.NewReader(reqBody2))
	res2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()

	res, err = http.Get(ts.URL + "/?protocol=http&count=0")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var allHttp []ServiceDescription
	if err := json.NewDecoder(res.Body).Decode(&allHttp); err != nil {
		t.Fatal(err)
	}
	if len(allHttp) != 2 {
		t.Fatalf("expected 2 results for count=0, got %d", len(allHttp))
	}
}
