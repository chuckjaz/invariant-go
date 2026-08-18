package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"invariant/internal/discovery"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func (m *mockDiscovery) Find(ctx context.Context, protocol, tag string, count int) ([]discovery.ServiceDescription, error) {
	return nil, nil // Not needed for this test
}

func (m *mockDiscovery) Get(ctx context.Context, id string) (discovery.ServiceDescription, bool) {
	desc, ok := m.services[id]
	return desc, ok
}

func (m *mockDiscovery) Register(ctx context.Context, reg discovery.ServiceRegistration) error {
	return nil
}

func TestStorageServer_Fetch(t *testing.T) {
	// Source server (the remote node that has the data)
	sourceStorage := NewInMemoryStorage()
	sourceContent := []byte("remote data block")
	sourceHash := sha256.Sum256(sourceContent)
	sourceAddr := hex.EncodeToString(sourceHash[:])
	sourceStorage.StoreAt(context.Background(), sourceAddr, bytes.NewReader(sourceContent))

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

	// 2. Fetch from source ID using Client.Fetch
	destClient := NewClient(destTS.URL, destTS.Client())
	err = destClient.Fetch(context.Background(), sourceAddr, sourceID, "")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	// 3. Verify destination has the block
	data, ok := destStorage.Get(context.Background(), sourceAddr)
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
	err = destClient.Fetch(context.Background(), badAddr, "missing-node-id", "")
	if err == nil {
		t.Error("expected fetch of missing node to return error")
	}

	// 5. Fetch using fallback address
	// Clear block from destStorage so we can test retrieving it again via fallback
	destStorage.Remove(context.Background(), sourceAddr)
	err = destClient.Fetch(context.Background(), sourceAddr, "missing-node-id-with-fallback", sourceTS.URL)
	if err != nil {
		t.Fatalf("Fetch with fallback failed: %v", err)
	}
	if !destStorage.Has(context.Background(), sourceAddr) {
		t.Error("expected destination to store block retrieved via fallback")
	}
}

// nonBatchServerStorage wraps Storage but hides BatchStorage interface.
type nonBatchServerStorage struct {
	Storage
}

func TestStorageServer_Batch(t *testing.T) {
	ctx := context.Background()

	// 1. Test with a backend that implements BatchStorage (InMemoryStorage)
	storageImpl := NewInMemoryStorage()
	server := NewStorageServer(storageImpl)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())

	content1 := []byte("batch-payload-1")
	hash1 := sha256.Sum256(content1)
	addr1 := hex.EncodeToString(hash1[:])

	content2 := []byte("batch-payload-2")
	hash2 := sha256.Sum256(content2)
	addr2 := hex.EncodeToString(hash2[:])

	blocks := map[string]io.Reader{
		addr1: bytes.NewReader(content1),
		addr2: bytes.NewReader(content2),
	}

	err := client.BatchStore(ctx, blocks)
	if err != nil {
		t.Fatalf("BatchStore failed: %v", err)
	}

	missing, err := client.BatchHas(ctx, []string{addr1, addr2, "b3"})
	if err != nil {
		t.Fatalf("BatchHas failed: %v", err)
	}
	if len(missing) != 1 || missing[0] != "b3" {
		t.Errorf("Expected missing to be ['b3'], got %v", missing)
	}

	// 2. Test with a backend that does NOT implement BatchStorage
	nonBatchImpl := &nonBatchServerStorage{storageImpl}
	server2 := NewStorageServer(nonBatchImpl)
	ts2 := httptest.NewServer(server2.Handler())
	defer ts2.Close()

	client2 := NewClient(ts2.URL, ts2.Client())

	content3 := []byte("batch-payload-4")
	hash3 := sha256.Sum256(content3)
	addr3 := hex.EncodeToString(hash3[:])

	blocks2 := map[string]io.Reader{
		addr3: bytes.NewReader(content3),
	}

	err = client2.BatchStore(ctx, blocks2)
	if err != nil {
		t.Fatalf("BatchStore (non-batch server) failed: %v", err)
	}

	missing2, err := client2.BatchHas(ctx, []string{addr3, "b5"})
	if err != nil {
		t.Fatalf("BatchHas (non-batch server) failed: %v", err)
	}
	if len(missing2) != 1 || missing2[0] != "b5" {
		t.Errorf("Expected missing to be ['b5'], got %v", missing2)
	}
}

type mockNotifyClient struct {
	notified chan []string
}

func (m *mockNotifyClient) Notify(id string, batch []string) error {
	m.notified <- batch
	return nil
}

func TestStorageServer_StartNotification(t *testing.T) {
	ctx := t.Context()

	memStore := NewInMemoryStorage()
	server := NewStorageServer(memStore)

	// Pre-load a block
	content := []byte("notify content")
	addr, err := memStore.Store(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	client := &mockNotifyClient{
		notified: make(chan []string, 10),
	}

	// Start notifications with batchSize=1, duration=10ms
	server.StartNotification(ctx, []NotifyClient{client}, 1, 10*time.Millisecond)

	// 1. Should notify about the existing block immediately
	select {
	case batch := <-client.notified:
		if len(batch) != 1 || batch[0] != addr {
			t.Errorf("Expected initial notification for %s, got %v", addr, batch)
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("Timed out waiting for initial notification")
	}

	// 2. Store another block, should notify via subscribe
	content2 := []byte("notify content 2")
	addr2, err := memStore.Store(ctx, bytes.NewReader(content2))
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	select {
	case batch := <-client.notified:
		if len(batch) != 1 || batch[0] != addr2 {
			t.Errorf("Expected live notification for %s, got %v", addr2, batch)
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("Timed out waiting for live notification")
	}
}
