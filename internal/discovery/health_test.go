package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthTracker_SortingAndEviction(t *testing.T) {
	// Create two fake services
	// 1. Healthy: returns its ID
	tsHealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/id" {
			w.Write([]byte("node1"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer tsHealthy.Close()

	// 2. Unhealthy: returns wrong ID
	tsUnhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/id" {
			w.Write([]byte("wrong-id"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer tsUnhealthy.Close()

	interval := 50 * time.Millisecond
	timeout := 200 * time.Millisecond

	d := NewInMemoryDiscovery().WithHealthTracking(interval, timeout)
	defer d.Close()

	ctx := context.Background()
	addr1 := strings.TrimPrefix(tsHealthy.URL, "http://")
	addr2 := strings.TrimPrefix(tsUnhealthy.URL, "http://")

	d.Register(ctx, ServiceRegistration{ID: "node1", Address: addr1, Protocols: []string{"test-proto"}})
	d.Register(ctx, ServiceRegistration{ID: "node2", Address: addr2, Protocols: []string{"test-proto"}})

	res, _ := d.Find(ctx, "test-proto", 10)
	if len(res) != 2 {
		t.Fatalf("expected 2 services, got %d", len(res))
	}

	// Wait for health check to run and mark node2 as unhealthy
	time.Sleep(100 * time.Millisecond)

	res, _ = d.Find(ctx, "test-proto", 10)
	if len(res) != 2 {
		t.Fatalf("expected 2 services before eviction, got %d", len(res))
	}
	// The healthy one must be first
	if res[0].ID != "node1" {
		t.Errorf("expected node1 to be first (healthy), got %v", res[0].ID)
	}

	// Wait for eviction timeout
	time.Sleep(500 * time.Millisecond)

	res, _ = d.Find(ctx, "test-proto", 10)
	if len(res) != 1 {
		t.Fatalf("expected 1 service after eviction, got %d", len(res))
	}
	if res[0].ID != "node1" {
		t.Errorf("expected remaining service to be node1, got %v", res[0].ID)
	}
}
