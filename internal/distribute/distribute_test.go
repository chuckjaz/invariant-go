package distribute

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"invariant/internal/discovery"
)

func TestDistance(t *testing.T) {
	a, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000001")
	b, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000002")
	expected, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000003")

	dist := Distance(a, b)
	if !bytes.Equal(dist, expected) {
		t.Errorf("expected %x, got %x", expected, dist)
	}
}

func TestCmpDistance(t *testing.T) {
	d1, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000001")
	d2, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000002")

	if CmpDistance(d1, d2) != -1 {
		t.Errorf("expected d1 < d2")
	}
	if CmpDistance(d2, d1) != 1 {
		t.Errorf("expected d2 > d1")
	}
	if CmpDistance(d1, d1) != 0 {
		t.Errorf("expected d1 == d1")
	}
}

func TestPrefixLen(t *testing.T) {
	dist := []byte{0x00, 0x00, 0x0f, 0xff}
	// 16 bits of 0 + 4 bits of 0 in 0x0f = 20 bits of 0
	expected := 20
	res := PrefixLen(dist)
	if res != expected {
		t.Errorf("expected PrefixLen %d, got %d", expected, res)
	}

	allZeros := []byte{0x00, 0x00}
	if PrefixLen(allZeros) != 16 {
		t.Errorf("expected 16, got %d", PrefixLen(allZeros))
	}
}

func TestMismatchedDistanceInputs(t *testing.T) {
	if Distance([]byte{1}, []byte{1, 2}) != nil {
		t.Error("expected nil for mismatched sizes in Distance")
	}

	if CmpDistance([]byte{1}, []byte{1, 2}) != 0 {
		t.Error("expected 0 for mismatched sizes in CmpDistance")
	}
}

func TestClientRegister(t *testing.T) {
	// 1. NewClient with nil client
	c1 := NewClient("http://localhost:12345", nil)
	if c1 == nil {
		t.Fatal("expected client not to be nil")
	}

	// 2. Register success
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/register/node1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c2 := NewClient(server.URL, server.Client())
	err := c2.Register("node1")
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// 3. Register server error
	serverErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer serverErr.Close()

	c3 := NewClient(serverErr.URL, serverErr.Client())
	err = c3.Register("node1")
	if err == nil {
		t.Error("expected error for HTTP 500, got nil")
	}

	// 4. Register HTTP error (bad port)
	c4 := NewClient("http://[::1]:invalidport", nil)
	err = c4.Register("node1")
	if err == nil {
		t.Error("expected error for invalid port, got nil")
	}
}

type mockDiscoveryLocal struct {
	services []discovery.ServiceDescription
}

func (m *mockDiscoveryLocal) Find(ctx context.Context, protocol string, count int) ([]discovery.ServiceDescription, error) {
	return m.services, nil
}

func (m *mockDiscoveryLocal) Get(ctx context.Context, id string) (discovery.ServiceDescription, bool) {
	for _, s := range m.services {
		if s.ID == id {
			return s, true
		}
	}
	return discovery.ServiceDescription{}, false
}

func (m *mockDiscoveryLocal) Register(ctx context.Context, reg discovery.ServiceRegistration) error {
	return nil
}

func TestInMemoryDistribute_SyncToDestination(t *testing.T) {
	var mu sync.Mutex
	sourceHeadReqs := 0
	destFetchReqs := 0

	// Source storage node: handles HEAD /<block>
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "1024")
			w.WriteHeader(http.StatusOK)
			mu.Lock()
			sourceHeadReqs++
			mu.Unlock()
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer sourceServer.Close()

	// Destination backup storage node: handles POST /fetch
	destServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/fetch" {
			w.WriteHeader(http.StatusOK)
			mu.Lock()
			destFetchReqs++
			mu.Unlock()
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer destServer.Close()

	// Set up mock discovery containing source and destination
	disc := &mockDiscoveryLocal{
		services: []discovery.ServiceDescription{
			{ID: "source-node-id-000000000000000000000000000000000000000000000000000", Address: sourceServer.URL, Protocols: []string{"storage-v1"}},
			{ID: "backup-node-id-000000000000000000000000000000000000000000000000000", Address: destServer.URL, Protocols: []string{"storage-v1"}},
		},
	}

	d := NewInMemoryDistribute(disc, 1, 1, "backup-node-id-000000000000000000000000000000000000000000000000000", 10.0)

	// Register source node
	d.Register(context.Background(), "source-node-id-000000000000000000000000000000000000000000000000000")

	// Notify about block
	blockID := "block-id-12345"
	d.Notify(context.Background(), "source-node-id-000000000000000000000000000000000000000000000000000", []string{blockID})

	// Perform Sync
	d.Sync()

	mu.Lock()
	sh := sourceHeadReqs
	df := destFetchReqs
	mu.Unlock()

	if sh != 1 {
		t.Errorf("Expected 1 HEAD request to source, got %d", sh)
	}
	if df != 1 {
		t.Errorf("Expected 1 POST /fetch request to destination, got %d", df)
	}

	// Verify StartSync runs without panicking/blocking
	d.StartSync(1 * time.Millisecond)
	time.Sleep(5 * time.Millisecond)
}
