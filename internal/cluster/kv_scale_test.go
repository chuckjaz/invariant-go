//go:build cluster

package cluster

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"invariant/internal/kv"
)

type kvLocalRoundTripper struct {
	mu       sync.RWMutex
	handlers map[string]http.Handler
}

func (l *kvLocalRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	l.mu.RLock()
	h, ok := l.handlers[req.URL.Host]
	l.mu.RUnlock()
	if !ok {
		return http.DefaultTransport.RoundTrip(req)
	}

	var bodyBytes []byte
	if req.Body != nil {
		bodyBytes, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	resp := rec.Result()
	return resp, nil
}

func (l *kvLocalRoundTripper) RegisterHandler(host string, h http.Handler) {
	l.mu.Lock()
	l.handlers[host] = h
	l.mu.Unlock()
}

func TestScale_KeyValueService(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping KV scale integration test in short mode")
	}

	rt := &kvLocalRoundTripper{
		handlers: make(map[string]http.Handler),
	}
	oldTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = rt
	defer func() {
		http.DefaultClient.Transport = oldTransport
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cl, err := NewCluster()
	if err != nil {
		t.Fatalf("failed to create cluster: %v", err)
	}
	defer cl.Close()
	cl.Registry = rt
	cl.UseInMemoryStorage = true

	// 1. Start central services: Discovery, Slots, Finder, Distribute
	mDiscovery, err := cl.NewMachine("machine-discovery")
	if err != nil {
		t.Fatalf("failed to create machine-discovery: %v", err)
	}
	discURL, err := mDiscovery.StartDiscovery(ctx)
	if err != nil {
		t.Fatalf("failed to start discovery: %v", err)
	}

	mSlots, err := cl.NewMachine("machine-slots")
	if err != nil {
		t.Fatalf("failed to create machine-slots: %v", err)
	}
	slotsURL, err := mSlots.StartSlots(ctx, discURL)
	if err != nil {
		t.Fatalf("failed to start slots: %v", err)
	}

	mFinder, err := cl.NewMachine("machine-finder")
	if err != nil {
		t.Fatalf("failed to create machine-finder: %v", err)
	}
	finderURL, err := mFinder.StartFinder(ctx, discURL)
	if err != nil {
		t.Fatalf("failed to start finder: %v", err)
	}

	mDistribute, err := cl.NewMachine("machine-distribute")
	if err != nil {
		t.Fatalf("failed to create machine-distribute: %v", err)
	}
	distURL, err := mDistribute.StartDistribute(ctx, discURL, 3, 3)
	if err != nil {
		t.Fatalf("failed to start distribute: %v", err)
	}

	// 2. Start 20 storage services
	t.Log("Starting 20 storage services...")
	const numStorage = 20
	storageMachines := make([]*Machine, numStorage)
	var wg sync.WaitGroup
	errs := make(chan error, numStorage)
	for i := range numStorage {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("machine-storage-%d", i)
			m, err := cl.NewMachine(name)
			if err != nil {
				errs <- fmt.Errorf("failed to create %s: %w", name, err)
				return
			}
			_, err = m.StartStorage(ctx, discURL, distURL, finderURL)
			if err != nil {
				errs <- fmt.Errorf("failed to start storage on %s: %w", name, err)
				return
			}
			storageMachines[i] = m
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Failed to initialize storage cluster: %v", err)
		}
	}

	// Wait for registrations to settle
	time.Sleep(200 * time.Millisecond)

	// 3. Start Key-Value service
	t.Log("Starting Key-Value service...")
	mKV, err := cl.NewMachine("machine-kv")
	if err != nil {
		t.Fatalf("failed to create machine-kv: %v", err)
	}
	mKV.BTreeMergeThreshold = 50000
	mKV.JournalFlushThreshold = 5000

	btreeSlotID := "0000000000000000000000000000000000000000000000000000000000000001"
	journalSlotID := "0000000000000000000000000000000000000000000000000000000000000002"
	kvURL, err := mKV.StartKV(ctx, discURL, slotsURL, finderURL, btreeSlotID, journalSlotID)
	if err != nil {
		t.Fatalf("failed to start KV service: %v", err)
	}

	// 4. Create random entries in batches
	const totalEntries = 1000000
	const batchSize = 5000
	t.Logf("Creating %d entries...", totalEntries)
	kvClient := kv.NewClient(kvURL, nil)

	var wgPut sync.WaitGroup
	putErrs := make(chan error, totalEntries/batchSize)
	sem := make(chan struct{}, 8)

	for i := 0; i < totalEntries; i += batchSize {
		wgPut.Add(1)
		go func(start int) {
			defer wgPut.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			kvs := make(map[string][]byte)
			for j := range batchSize {
				key := fmt.Sprintf("k-%d", start+j)
				val := fmt.Appendf(nil, "v-%d", start+j)
				kvs[key] = val
			}

			_, err := kvClient.BatchPut(ctx, nil, kvs)
			if err != nil {
				putErrs <- fmt.Errorf("BatchPut failed at start %d: %w", start, err)
			}
		}(i)
	}
	wgPut.Wait()
	close(putErrs)
	for err := range putErrs {
		if err != nil {
			t.Fatalf("KV writes failed: %v", err)
		}
	}
	t.Logf("Successfully wrote %d entries.", totalEntries)

	// 5. Terminate and restart the key-value service
	t.Log("Terminating key-value service...")
	mKV.StopService("kv")

	// Small pause to verify shutdown
	time.Sleep(100 * time.Millisecond)

	t.Log("Restarting key-value service...")
	kvURL, err = mKV.StartService(ctx, "kv")
	if err != nil {
		t.Fatalf("failed to restart KV service: %v", err)
	}

	// 6. Verify all entries are retrievable and match after restart
	t.Logf("Verifying all %d entries after restart...", totalEntries)
	kvClient2 := kv.NewClient(kvURL, nil)

	var wgGet sync.WaitGroup
	getErrs := make(chan error, totalEntries/batchSize)
	getSem := make(chan struct{}, 8)

	for i := 0; i < totalEntries; i += batchSize {
		wgGet.Add(1)
		go func(start int) {
			defer wgGet.Done()
			getSem <- struct{}{}
			defer func() { <-getSem }()

			keys := make([]string, batchSize)
			for j := range batchSize {
				keys[j] = fmt.Sprintf("k-%d", start+j)
			}

			results, err := kvClient2.BatchGet(ctx, nil, keys)
			if err != nil {
				getErrs <- fmt.Errorf("BatchGet failed at start %d: %w", start, err)
				return
			}

			for j := range batchSize {
				key := keys[j]
				expectedVal := fmt.Sprintf("v-%d", start+j)
				res, ok := results[key]
				if !ok {
					getErrs <- fmt.Errorf("expected key %s not found in results", key)
					return
				}
				if string(res.Value) != expectedVal {
					getErrs <- fmt.Errorf("value mismatch for key %s: expected %s, got %s", key, expectedVal, string(res.Value))
					return
				}
			}
		}(i)
	}
	wgGet.Wait()
	close(getErrs)
	for err := range getErrs {
		if err != nil {
			t.Fatalf("KV verification failed: %v", err)
		}
	}

	t.Logf("Success! All %d entries successfully verified after KV service restart!", totalEntries)
}
