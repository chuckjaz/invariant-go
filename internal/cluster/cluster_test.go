package cluster

import (
	"context"
	"strings"
	"testing"
	"time"

	"invariant/internal/storage"
)

func TestIntegration_ClusterResilience(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cl, err := NewCluster()
	if err != nil {
		t.Fatalf("failed to create cluster: %v", err)
	}
	defer cl.Close()

	// 1. Create machines
	mDiscovery, err := cl.NewMachine("machine-discovery")
	if err != nil {
		t.Fatalf("failed to create machine-discovery: %v", err)
	}

	mDistribute, err := cl.NewMachine("machine-distribute")
	if err != nil {
		t.Fatalf("failed to create machine-distribute: %v", err)
	}

	mStorage1, err := cl.NewMachine("machine-storage-1")
	if err != nil {
		t.Fatalf("failed to create machine-storage-1: %v", err)
	}

	mStorage2, err := cl.NewMachine("machine-storage-2")
	if err != nil {
		t.Fatalf("failed to create machine-storage-2: %v", err)
	}

	mStorage3, err := cl.NewMachine("machine-storage-3")
	if err != nil {
		t.Fatalf("failed to create machine-storage-3: %v", err)
	}

	// 2. Start services
	discURL, err := mDiscovery.StartDiscovery(ctx)
	if err != nil {
		t.Fatalf("failed to start discovery: %v", err)
	}

	distURL, err := mDistribute.StartDistribute(ctx, discURL, 3, 3) // repFactor = 3
	if err != nil {
		t.Fatalf("failed to start distribute: %v", err)
	}

	s1URL, err := mStorage1.StartStorage(ctx, discURL, distURL)
	if err != nil {
		t.Fatalf("failed to start storage 1: %v", err)
	}

	s2URL, err := mStorage2.StartStorage(ctx, discURL, distURL)
	if err != nil {
		t.Fatalf("failed to start storage 2: %v", err)
	}

	s3URL, err := mStorage3.StartStorage(ctx, discURL, distURL)
	if err != nil {
		t.Fatalf("failed to start storage 3: %v", err)
	}

	// Wait for services to advertise and register fully
	time.Sleep(500 * time.Millisecond)

	// 3. Store a block on storage-1
	c1 := storage.NewClient(s1URL, nil)
	c2 := storage.NewClient(s2URL, nil)
	c3 := storage.NewClient(s3URL, nil)

	data := "this is a test block for replication resilience"
	addr, err := c1.Store(ctx, strings.NewReader(data))
	if err != nil {
		t.Fatalf("failed to store block: %v", err)
	}

	// 4. Verify replication across all 3 nodes (repFactor = 3)
	// We wait up to 3 seconds for replication to occur.
	replicated := false
	for range 30 {
		if c1.Has(ctx, addr) && c2.Has(ctx, addr) && c3.Has(ctx, addr) {
			replicated = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !replicated {
		t.Errorf("Block was not replicated to all 3 storage nodes")
	}

	// 5. Bring down machine-storage-2 (simulate machine/disk failure)
	t.Logf("Stopping machine-storage-2...")
	mStorage2.StopAll()

	// Wait for discovery to potentially detect failure or for state to settle
	time.Sleep(500 * time.Millisecond)

	// 6. Write a new block to storage-1
	data2 := "another block while storage-2 is down"
	addr2, err := c1.Store(ctx, strings.NewReader(data2))
	if err != nil {
		t.Fatalf("failed to store second block: %v", err)
	}

	// Verify it replicated to storage-1 and storage-3, but NOT storage-2 (which is offline)
	replicated2 := false
	for range 20 {
		if c1.Has(ctx, addr2) && c3.Has(ctx, addr2) {
			replicated2 = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !replicated2 {
		t.Errorf("Second block was not replicated to active nodes storage-1 and storage-3")
	}

	if c2.Has(ctx, addr2) {
		t.Errorf("Offline storage-2 should not have the block")
	}

	// 7. Bring machine-storage-2 back online (retaining its data directory/identity)
	t.Logf("Starting machine-storage-2 back up...")
	_, err = mStorage2.StartService(ctx, "storage")
	if err != nil {
		t.Fatalf("failed to restart storage 2: %v", err)
	}

	// Wait for replication sync loop to pick it up and replicate the missing block
	replicatedToRestored := false
	for range 40 {
		if c2.Has(ctx, addr2) {
			replicatedToRestored = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !replicatedToRestored {
		t.Errorf("Restored storage-2 did not catch up and replicate the missing block")
	}

	t.Logf("Integration test completed successfully - service resilient to machine failure!")
}

func TestIntegration_FinderIDPersistence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cl, err := NewCluster()
	if err != nil {
		t.Fatalf("failed to create cluster: %v", err)
	}
	defer cl.Close()

	mDiscovery, err := cl.NewMachine("machine-discovery")
	if err != nil {
		t.Fatalf("failed to create machine-discovery: %v", err)
	}
	discURL, err := mDiscovery.StartDiscovery(ctx)
	if err != nil {
		t.Fatalf("failed to start discovery: %v", err)
	}

	mFinder, err := cl.NewMachine("machine-finder")
	if err != nil {
		t.Fatalf("failed to create machine-finder: %v", err)
	}

	_, err = mFinder.StartFinder(ctx, discURL)
	if err != nil {
		t.Fatalf("failed to start finder: %v", err)
	}

	id1 := mFinder.FinderNode().ID()
	if len(id1) != 64 {
		t.Errorf("Expected 64-char hex ID, got: %s", id1)
	}

	mFinder.StopService("finder")

	// Restart finder
	_, err = mFinder.StartService(ctx, "finder")
	if err != nil {
		t.Fatalf("failed to restart finder: %v", err)
	}

	id2 := mFinder.FinderNode().ID()
	if id1 != id2 {
		t.Errorf("Finder ID changed after restart: expected %s, got %s", id1, id2)
	}
}
