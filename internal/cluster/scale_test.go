package cluster

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"invariant/internal/distribute"
	"invariant/internal/finder"
	"invariant/internal/storage"
)

type LocalRoundTripper struct {
	mu       sync.RWMutex
	handlers map[string]http.Handler
}

func (l *LocalRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
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

func (l *LocalRoundTripper) RegisterHandler(host string, h http.Handler) {
	l.mu.Lock()
	l.handlers[host] = h
	l.mu.Unlock()
}

func getDistribute(distServer *distribute.DistributeServer) distribute.Distribute {
	type distributeServerLayout struct {
		id         string
		distribute distribute.Distribute
		handler    http.Handler
	}
	ptr := (*distributeServerLayout)(unsafe.Pointer(distServer))
	return ptr.distribute
}

func unregisterFromDistribute(d distribute.Distribute, nodeID string) {
	// Cast InMemoryDistribute to get access to its unexported services map
	type inMemoryDistributeLayout struct {
		mu       sync.RWMutex
		services map[string]unsafe.Pointer
	}
	layout := (*inMemoryDistributeLayout)(unsafe.Pointer(d.(*distribute.InMemoryDistribute)))
	layout.mu.Lock()
	defer layout.mu.Unlock()
	delete(layout.services, nodeID)
}

func countStorageBlocks(m *Machine) int {
	sNode := m.StorageNode()
	if sNode == nil {
		return 0
	}

	// Try casting as InMemoryStorage
	if inMem, ok := sNode.(*storage.InMemoryStorage); ok {
		type inMemoryStorageLayout struct {
			id    string
			mu    sync.RWMutex
			store map[string][]byte
		}
		layout := (*inMemoryStorageLayout)(unsafe.Pointer(inMem))
		layout.mu.RLock()
		defer layout.mu.RUnlock()
		return len(layout.store)
	}

	dir := filepath.Join(m.dataDir, "storage")
	count := 0
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() != "id" && !strings.HasPrefix(d.Name(), "upload-") {
			count++
		}
		return nil
	})
	return count
}

func waitWithTimeout(cond func() bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timeout reached")
}

func TestScale_StorageDistributeFinder(t *testing.T) {
	// Skip this test in short mode
	if testing.Short() {
		t.Skip("skipping scale integration test in short mode")
	}

	rt := &LocalRoundTripper{
		handlers: make(map[string]http.Handler),
	}
	oldTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = rt
	defer func() {
		http.DefaultClient.Transport = oldTransport
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	cl, err := NewCluster()
	if err != nil {
		t.Fatalf("failed to create cluster: %v", err)
	}
	defer cl.Close()
	cl.Registry = rt

	// Use InMemoryStorage for extreme speed and low footprint under heavy load
	cl.UseInMemoryStorage = true

	// 1. Start central services: Discovery, Distribute, Finder
	mDiscovery, err := cl.NewMachine("machine-discovery")
	if err != nil {
		t.Fatalf("failed to create machine-discovery: %v", err)
	}
	discURL, err := mDiscovery.StartDiscovery(ctx)
	if err != nil {
		t.Fatalf("failed to start discovery: %v", err)
	}

	mDistribute, err := cl.NewMachine("machine-distribute")
	if err != nil {
		t.Fatalf("failed to create machine-distribute: %v", err)
	}
	// Use replication factor of 6 to guarantee resilience when killing 10 nodes at a time
	distURL, err := mDistribute.StartDistribute(ctx, discURL, 6, 3)
	if err != nil {
		t.Fatalf("failed to start distribute: %v", err)
	}

	mFinder, err := cl.NewMachine("machine-finder")
	if err != nil {
		t.Fatalf("failed to create machine-finder: %v", err)
	}
	finderURL, err := mFinder.StartFinder(ctx, discURL)
	if err != nil {
		t.Fatalf("failed to start finder: %v", err)
	}

	// 2. Start 100 storage services
	t.Log("Starting 100 storage services...")
	const numStorage = 100
	storageMachines := make([]*Machine, numStorage)
	var wg sync.WaitGroup
	errs := make(chan error, numStorage)
	for i := 0; i < numStorage; i++ {
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
	time.Sleep(500 * time.Millisecond)

	// 3. Create 100,000 random blocks and write them directly to the storage nodes
	t.Log("Creating and storing 100,000 random blocks...")
	const numBlocks = 100000
	blockAddrs := make([]string, numBlocks)
	for i := 0; i < numBlocks; i++ {
		if i%10 == 0 {
			time.Sleep(10 * time.Microsecond)
		}
		content := fmt.Sprintf("b%d", i)
		nodeIndex := i % numStorage
		m := storageMachines[nodeIndex]

		addr, err := m.StorageNode().Store(ctx, strings.NewReader(content))
		if err != nil {
			t.Fatalf("failed to store block %d: %v", i, err)
		}
		blockAddrs[i] = addr
	}

	// 4. Wait for the finder to know about all 100,000 blocks
	t.Log("Waiting for finder to receive notifications for all 100,000 blocks...")
	memFinder := mFinder.FinderNode().(*finder.MemoryFinder)

	err = waitWithTimeout(func() bool {
		return len(memFinder.SnapshotBlocks()) == numBlocks
	}, 25*time.Second)
	if err != nil {
		t.Fatalf("finder did not receive all block notifications: %v, got %d", err, len(memFinder.SnapshotBlocks()))
	}
	t.Log("All 100,000 blocks successfully registered on the finder.")

	// Let distribute sync replicate them to repFactor = 6
	t.Log("Waiting for distribute sync to replicate blocks to target factor (6)...")
	err = waitWithTimeout(func() bool {
		totalReplicas := 0
		for _, m := range storageMachines {
			totalReplicas += countStorageBlocks(m)
		}
		return totalReplicas >= numBlocks*6
	}, 90*time.Second)
	if err != nil {
		totalReplicas := 0
		for _, m := range storageMachines {
			totalReplicas += countStorageBlocks(m)
		}
		t.Fatalf("replication failed to reach target factor: %v, got %d replicas", err, totalReplicas)
	}
	t.Log("Initial replication completed successfully.")

	// 5. Tear down original storage nodes randomly and replace them in batches of 10
	t.Log("Starting replacement of all 100 storage servers...")
	activeOriginals := make([]*Machine, numStorage)
	copy(activeOriginals, storageMachines)

	distServer := mDistribute.servers["distribute"].Handler.(*distribute.DistributeServer)
	distObj := getDistribute(distServer)

	round := 1
	newMachineIdx := 0
	for len(activeOriginals) > 0 {
		t.Logf("Replacement Round %d: replacing 10 servers...", round)
		var toReplace []*Machine
		for i := 0; i < 10 && len(activeOriginals) > 0; i++ {
			idx := rand.Intn(len(activeOriginals))
			toReplace = append(toReplace, activeOriginals[idx])
			activeOriginals = append(activeOriginals[:idx], activeOriginals[idx+1:]...)
		}

		// Stop and unregister selected nodes
		for _, m := range toReplace {
			sID := m.StorageID()
			m.StopService("storage")
			unregisterFromDistribute(distObj, sID)
		}

		// Wait for discovery health check eviction
		time.Sleep(600 * time.Millisecond)

		// Start 10 new storage servers
		var newMachines []*Machine
		for i := 0; i < 10; i++ {
			name := fmt.Sprintf("machine-storage-new-%d", newMachineIdx)
			newMachineIdx++
			m, err := cl.NewMachine(name)
			if err != nil {
				t.Fatalf("failed to create new machine %s: %v", name, err)
			}
			_, err = m.StartStorage(ctx, discURL, distURL, finderURL)
			if err != nil {
				t.Fatalf("failed to start storage on new machine %s: %v", name, err)
			}
			newMachines = append(newMachines, m)
			storageMachines = append(storageMachines, m)
		}

		// Wait for distribute sync loop to replicate blocks onto the new machines
		t.Log("Waiting for distribute sync to replicate blocks and maintain factor (6)...")
		err = waitWithTimeout(func() bool {
			totalReplicas := 0
			for _, m := range storageMachines {
				if m.servers["storage"] != nil {
					totalReplicas += countStorageBlocks(m)
				}
			}
			return totalReplicas >= numBlocks*6
		}, 35*time.Second)
		if err != nil {
			totalReplicas := 0
			for _, m := range storageMachines {
				if m.servers["storage"] != nil {
					totalReplicas += countStorageBlocks(m)
				}
			}
			t.Fatalf("replication failed after round %d replacement: %v, got %d replicas", round, err, totalReplicas)
		}

		round++
	}

	t.Log("All 100 original storage servers have been replaced.")

	// 6. Verify all blocks can still be found by the finder
	t.Log("Verifying all 100,000 blocks can still be found by the finder...")
	snap := memFinder.SnapshotBlocks()
	if len(snap) != numBlocks {
		t.Errorf("expected finder to know about %d blocks, got %d", numBlocks, len(snap))
	}

	missingCount := 0
	for _, addr := range blockAddrs {
		locations := snap[addr]
		if len(locations) == 0 {
			missingCount++
		}
	}

	if missingCount > 0 {
		t.Errorf("finder failed to find %d blocks after machine replacements!", missingCount)
	} else {
		t.Log("Success! All 100,000 blocks successfully located by the finder after complete cluster replacement!")
	}
}
