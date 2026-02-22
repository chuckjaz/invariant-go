package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"invariant/internal/discovery"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStorageServer(t *testing.T) {
	storage := NewInMemoryStorage()
	server := NewStorageServer(storage)
	ts := httptest.NewServer(server)
	defer ts.Close()

	// 1. GET /id
	res, err := http.Get(ts.URL + "/id")
	if err != nil {
		t.Fatal(err)
	}
	idBytes, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if len(idBytes) != 64 {
		t.Errorf("expected 64 char hex string for /id, got length %d", len(idBytes))
	}

	// 2. POST /
	content := []byte("hello world")
	res, err = http.Post(ts.URL+"/", "text/plain", bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	addressBytes, _ := io.ReadAll(res.Body)
	res.Body.Close()
	address := string(addressBytes)

	hash1 := sha256.Sum256(content)
	expectedHash := hex.EncodeToString(hash1[:])
	if address != expectedHash {
		t.Errorf("expected hash %s, got %s", expectedHash, address)
	}

	// 3. GET /:address
	res, err = http.Get(ts.URL + "/" + address)
	if err != nil {
		t.Fatal(err)
	}
	if res.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("expected application/octet-stream, got %s", res.Header.Get("Content-Type"))
	}
	if res.Header.Get("ETag") != expectedHash {
		t.Errorf("expected ETag %s, got %s", expectedHash, res.Header.Get("ETag"))
	}
	valBytes, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(valBytes) != string(content) {
		t.Errorf("expected %s, got %s", content, valBytes)
	}

	// 4. HEAD /:address
	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/"+address, nil)
	client := &http.Client{}
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", res.StatusCode)
	}
	if res.Header.Get("Content-Length") != "11" {
		t.Errorf("expected Content-Length 11, got %s", res.Header.Get("Content-Length"))
	}

	// 5. PUT /:address
	newContent := []byte("new content")
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/"+address, bytes.NewReader(newContent))
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", res.StatusCode)
	}

	hash2 := sha256.Sum256(newContent)
	newExpectedHash := hex.EncodeToString(hash2[:])
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/"+newExpectedHash, bytes.NewReader(newContent))
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", res.StatusCode)
	}
	if string(body) != newExpectedHash {
		t.Errorf("expected %s, got %s", newExpectedHash, string(body))
	}

	// 6. Test /fetch optional endpoints
	res, _ = http.Post(ts.URL+"/fetch", "application/json", strings.NewReader(`{}`))
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 Not Found, got %d", res.StatusCode)
	}

	req, _ = http.NewRequest("HEAD", ts.URL+"/fetch", nil)
	res, _ = client.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 Not Found, got %d", res.StatusCode)
	}
}

// mockDiscovery is a simple mock discovery service for testing
type mockDiscovery struct {
	services map[string]discovery.ServiceDescription
}

func (m *mockDiscovery) Find(protocol string, count int) ([]discovery.ServiceDescription, error) {
	return nil, nil // Not needed for this test
}

func (m *mockDiscovery) Get(id string) (discovery.ServiceDescription, bool) {
	desc, ok := m.services[id]
	return desc, ok
}

func (m *mockDiscovery) Register(reg discovery.ServiceRegistration) error {
	return nil
}

func TestStorageServer_Fetch(t *testing.T) {
	// Source server (the remote node that has the data)
	sourceStorage := NewInMemoryStorage()
	sourceContent := []byte("remote data block")
	sourceHash := sha256.Sum256(sourceContent)
	sourceAddr := hex.EncodeToString(sourceHash[:])
	sourceStorage.StoreAt(sourceAddr, bytes.NewReader(sourceContent))

	sourceServer := NewStorageServer(sourceStorage)
	sourceTS := httptest.NewServer(sourceServer)
	defer sourceTS.Close()

	sourceID := "remote-node-id-12345"
	disc := &mockDiscovery{
		services: map[string]discovery.ServiceDescription{
			sourceID: {ID: sourceID, Address: sourceTS.URL},
		},
	}

	// Destination server (the one we tell to fetch)
	destStorage := NewInMemoryStorage()
	destServer := NewStorageServer(destStorage).WithDiscovery(disc)
	destTS := httptest.NewServer(destServer)
	defer destTS.Close()

	// 1. Send HEAD to /fetch (should be 200 OK because we have discovery)
	reqHead, _ := http.NewRequest("HEAD", destTS.URL+"/fetch", nil)
	client := &http.Client{}
	resHead, err := client.Do(reqHead)
	if err != nil {
		t.Fatal(err)
	}
	resHead.Body.Close()
	if resHead.StatusCode != http.StatusOK {
		t.Errorf("expected HEAD /storage/fetch to return 200 OK, got %d", resHead.StatusCode)
	}

	// 2. Fetch from source ID
	fetchReqBody := `{"address":"` + sourceAddr + `","container":"` + sourceID + `"}`
	resFetch, err := http.Post(destTS.URL+"/fetch", "application/json", strings.NewReader(fetchReqBody))
	if err != nil {
		t.Fatal(err)
	}
	resFetch.Body.Close()
	if resFetch.StatusCode != http.StatusOK {
		t.Errorf("expected POST /storage/fetch to return 200 OK, got %d", resFetch.StatusCode)
	}

	// 3. Verify destination has the block
	data, ok := destStorage.Get(sourceAddr)
	if !ok {
		t.Fatalf("destination storage did not save the fetched block")
	}
	defer data.Close()
	valBytes, _ := io.ReadAll(data)
	if string(valBytes) != string(sourceContent) {
		t.Errorf("expected fetched data to be %q, got %q", string(sourceContent), string(valBytes))
	}

	// 4. Fetch missing ID (should fail)
	// Use a new arbitrary address so the local storage optimization logic doesn't return 200 early.
	badAddr := "0101010101010101010101010101010101010101010101010101010101010101"
	badFetchReqBody := `{"address":"` + badAddr + `","container":"missing-node-id"}`
	resBadFetch, _ := http.Post(destTS.URL+"/fetch", "application/json", strings.NewReader(badFetchReqBody))
	resBadFetch.Body.Close()
	if resBadFetch.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502 Bad Gateway for missing node, got %d", resBadFetch.StatusCode)
	}
}
