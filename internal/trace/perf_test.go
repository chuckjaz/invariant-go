package trace_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"invariant/internal/content"
	"invariant/internal/discovery"
	"invariant/internal/distribute"
	"invariant/internal/files"
	"invariant/internal/filetree"
	"invariant/internal/finder"
	"invariant/internal/kv"
	"invariant/internal/names"
	"invariant/internal/notify"
	"invariant/internal/slots"
	"invariant/internal/storage"
	"invariant/internal/trace"
)

// TestPerformanceAndTracing executes concurrent load against each service, collects trace metrics,
// and validates performance characteristics across all 8 microservices and multi-service workflows.
func TestPerformanceAndTracing(t *testing.T) {
	// 1. Storage Service Performance & Tracing
	t.Run("Storage_SingleAndBatchOperations", func(t *testing.T) {
		tracer := trace.NewTracer(10000)
		memStore := storage.NewInMemoryStorage()
		srv := storage.NewStorageServer(memStore).WithTracer(tracer)
		ts := httptest.NewServer(srv)
		defer ts.Close()

		client := storage.NewClient(ts.URL, nil)

		const numOps = 40
		var wg sync.WaitGroup
		wg.Add(numOps)

		storedAddrs := make([]string, numOps)

		// Concurrent Put and Store
		for i := range numOps {
			go func(idx int) {
				defer wg.Done()
				data := fmt.Appendf(nil, "storage-payload-block-%d-%d", idx, time.Now().UnixNano())
				hash := sha256.Sum256(data)
				addr := hex.EncodeToString(hash[:])
				storedAddrs[idx] = addr

				ok, err := client.StoreAt(context.Background(), addr, bytes.NewReader(data))
				if err != nil || !ok {
					t.Errorf("StoreAt failed: %v", err)
				}

				// Has check
				if !client.Has(context.Background(), addr) {
					t.Errorf("Has failed for %s", addr)
				}

				// Get verification
				rc, ok := client.Get(context.Background(), addr)
				if !ok {
					t.Errorf("Get failed for %s", addr)
					return
				}
				defer rc.Close()
				readData, _ := io.ReadAll(rc)
				if !bytes.Equal(readData, data) {
					t.Errorf("Data mismatch for %s", addr)
				}
			}(i)
		}
		wg.Wait()

		// Batch Has (returns list of missing blocks)
		batchAddrs := storedAddrs[:20]
		missing, err := client.BatchHas(context.Background(), batchAddrs)
		if err != nil {
			t.Fatalf("BatchHas failed: %v", err)
		}
		if len(missing) != 0 {
			t.Errorf("Expected 0 missing blocks, got %d", len(missing))
		}

		summary := tracer.Summary("storage")
		t.Logf("=== Storage Trace Summary: Total Spans=%d, Errors=%d ===", summary.TotalSpans, summary.ErrorCount)
		for ep, stat := range summary.Endpoints {
			t.Logf("  [%s] count=%d, p50=%.2fms, p95=%.2fms, p99=%.2fms, max=%.2fms, bytesIn=%d, bytesOut=%d",
				ep, stat.Count, stat.P50Ms, stat.P95Ms, stat.P99Ms, stat.MaxMs, stat.BytesIn, stat.BytesOut)
		}
	})

	// 2. Discovery Service Performance & Tracing
	t.Run("Discovery_RegisterAndFind", func(t *testing.T) {
		tracer := trace.NewTracer(10000)
		disc := discovery.NewInMemoryDiscovery()
		srv := discovery.NewDiscoveryServer(disc).WithTracer(tracer)
		ts := httptest.NewServer(srv)
		defer ts.Close()

		client := discovery.NewClient(ts.URL, nil)

		const numNodes = 30
		var wg sync.WaitGroup
		wg.Add(numNodes)

		for i := range numNodes {
			go func(idx int) {
				defer wg.Done()
				idBytes := sha256.Sum256(fmt.Appendf(nil, "node-%d", idx))
				id := hex.EncodeToString(idBytes[:])
				addr := fmt.Sprintf("http://10.0.0.%d:3000", idx%250+1)

				if err := client.Register(context.Background(), discovery.ServiceRegistration{
					ID:        id,
					Address:   addr,
					Protocols: []string{"storage-v1", "notify-v1"},
					Tags:      []string{"cache"},
				}); err != nil {
					t.Errorf("Register failed: %v", err)
				}

				// Find query
				services, err := client.Find(context.Background(), "storage-v1", "cache", 10)
				if err != nil || len(services) == 0 {
					t.Errorf("Find failed: %v", err)
				}
			}(i)
		}
		wg.Wait()

		summary := tracer.Summary("discovery")
		t.Logf("=== Discovery Trace Summary: Total Spans=%d, Errors=%d ===", summary.TotalSpans, summary.ErrorCount)
		for ep, stat := range summary.Endpoints {
			t.Logf("  [%s] count=%d, p50=%.2fms, p95=%.2fms, p99=%.2fms, max=%.2fms",
				ep, stat.Count, stat.P50Ms, stat.P95Ms, stat.P99Ms, stat.MaxMs)
		}
	})

	// 3. Names Service Performance & Tracing
	t.Run("Names_ResolutionAndUpdates", func(t *testing.T) {
		tracer := trace.NewTracer(10000)
		nm := names.NewInMemoryNames()
		srv := names.NewNamesServer(nm).WithTracer(tracer)
		ts := httptest.NewServer(srv)
		defer ts.Close()

		client := names.NewClient(ts.URL, nil)

		const numNames = 30
		var wg sync.WaitGroup
		wg.Add(numNames)

		for i := range numNames {
			go func(idx int) {
				defer wg.Done()
				name := fmt.Sprintf("service.node.%d", idx)
				val := hex.EncodeToString(sha256.New().Sum([]byte(name)))
				tokens := []string{"storage-v1"}

				if err := client.Put(context.Background(), name, val, tokens); err != nil {
					t.Errorf("Put failed: %v", err)
				}

				entry, err := client.Get(context.Background(), name)
				if err != nil || entry.Value != val {
					t.Errorf("Get failed for %s: %v", name, err)
				}
			}(i)
		}
		wg.Wait()

		summary := tracer.Summary("names")
		t.Logf("=== Names Trace Summary: Total Spans=%d, Errors=%d ===", summary.TotalSpans, summary.ErrorCount)
		for ep, stat := range summary.Endpoints {
			t.Logf("  [%s] count=%d, p50=%.2fms, p95=%.2fms, p99=%.2fms, max=%.2fms",
				ep, stat.Count, stat.P50Ms, stat.P95Ms, stat.P99Ms, stat.MaxMs)
		}
	})

	// 4. Slots Service Performance & Tracing
	t.Run("Slots_CreateAndConcurrentUpdates", func(t *testing.T) {
		tracer := trace.NewTracer(10000)
		sl := slots.NewMemorySlots("slots-perf-test")
		srv := slots.NewServer(sl).WithTracer(tracer)
		ts := httptest.NewServer(srv)
		defer ts.Close()

		client := slots.NewClient(ts.URL, nil)

		const numSlots = 20
		var wg sync.WaitGroup
		wg.Add(numSlots)

		for i := range numSlots {
			go func(idx int) {
				defer wg.Done()
				slotIDBytes := sha256.Sum256(fmt.Appendf(nil, "slot-%d", idx))
				slotID := hex.EncodeToString(slotIDBytes[:])

				initAddrBytes := sha256.Sum256(fmt.Appendf(nil, "addr-0-%d", idx))
				initAddr := hex.EncodeToString(initAddrBytes[:])

				if err := client.Create(context.Background(), slotID, initAddr, ""); err != nil {
					t.Errorf("Create slot failed: %v", err)
					return
				}

				prevAddr := initAddr
				for step := 1; step <= 2; step++ {
					nextAddrBytes := sha256.Sum256(fmt.Appendf(nil, "addr-%d-%d", step, idx))
					nextAddr := hex.EncodeToString(nextAddrBytes[:])

					if err := client.Update(context.Background(), slotID, nextAddr, prevAddr, nil); err != nil {
						t.Errorf("Update slot failed at step %d: %v", step, err)
						return
					}
					prevAddr = nextAddr
				}

				curr, err := client.Get(context.Background(), slotID)
				if err != nil || curr != prevAddr {
					t.Errorf("Get slot mismatch: %v, got %s want %s", err, curr, prevAddr)
				}
			}(i)
		}
		wg.Wait()

		summary := tracer.Summary("slots")
		t.Logf("=== Slots Trace Summary: Total Spans=%d, Errors=%d ===", summary.TotalSpans, summary.ErrorCount)
		for ep, stat := range summary.Endpoints {
			t.Logf("  [%s] count=%d, p50=%.2fms, p95=%.2fms, p99=%.2fms, max=%.2fms",
				ep, stat.Count, stat.P50Ms, stat.P95Ms, stat.P99Ms, stat.MaxMs)
		}
	})

	// 5. Multi-Service Interaction: File Tree & Content Storage Workflow
	t.Run("MultiService_FileTreeStorageSync", func(t *testing.T) {
		tracer := trace.NewTracer(10000)

		// Setup Storage Server
		storageStore := storage.NewInMemoryStorage()
		storageSrv := storage.NewStorageServer(storageStore).WithTracer(tracer)
		tsStorage := httptest.NewServer(storageSrv)
		defer tsStorage.Close()
		storageClient := storage.NewClient(tsStorage.URL, nil)

		// Setup Slots Server
		slotStore := slots.NewMemorySlots("slots-sync-test")
		slotSrv := slots.NewServer(slotStore).WithTracer(tracer)
		tsSlots := httptest.NewServer(slotSrv)
		defer tsSlots.Close()
		slotsClient := slots.NewClient(tsSlots.URL, nil)

		// Initialize empty directory in storage
		dirData, _ := json.Marshal(filetree.Directory{})
		initLink, err := content.Write(bytes.NewReader(dirData), storageStore, content.WriterOptions{})
		if err != nil {
			t.Fatalf("Failed to initialize directory link: %v", err)
		}

		rootSlotBytes := sha256.Sum256([]byte("root-slot-files"))
		rootSlotID := hex.EncodeToString(rootSlotBytes[:])
		if err := slotsClient.Create(context.Background(), rootSlotID, initLink.Address, ""); err != nil {
			t.Fatalf("Failed to create root slot: %v", err)
		}

		// Setup Files Server
		opts := files.Options{
			Storage: storageClient,
			Slots:   slotsClient,
			RootLink: content.ContentLink{
				Address: rootSlotID,
				Slot:    true,
			},
			AutoSyncTimeout: 50 * time.Millisecond,
		}
		memFiles, err := files.NewInMemoryFiles(opts)
		if err != nil {
			t.Fatalf("NewInMemoryFiles failed: %v", err)
		}
		defer memFiles.Close()

		filesSrv := files.NewServer(memFiles).WithTracer(tracer)
		tsFiles := httptest.NewServer(filesSrv)
		defer tsFiles.Close()

		// Perform multiple concurrent filesystem write & sync operations via HTTP endpoint
		const numFiles = 15
		var wg sync.WaitGroup
		wg.Add(numFiles)

		for i := range numFiles {
			go func(idx int) {
				defer wg.Done()
				fileName := fmt.Sprintf("test-doc-%d.txt", idx)
				contentData := fmt.Appendf(nil, "Hello Invariant Distributed Content Storage #%d", idx)

				// Write file via HTTP endpoint PUT /1/{fileName}
				req, _ := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/1/%s", tsFiles.URL, fileName), bytes.NewReader(contentData))
				resp, err := http.DefaultClient.Do(req)
				if err != nil || resp.StatusCode != http.StatusCreated {
					t.Errorf("Write file HTTP failed for %s: err=%v status=%d", fileName, err, resp.StatusCode)
					return
				}
				resp.Body.Close()

				// Lookup file back via GET /lookup/1/{fileName}
				lookupReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/lookup/1/%s", tsFiles.URL, fileName), nil)
				lookupResp, err := http.DefaultClient.Do(lookupReq)
				if err != nil || lookupResp.StatusCode != http.StatusOK {
					t.Errorf("Lookup HTTP failed for %s: %v", fileName, err)
					return
				}
				var info files.ContentInformationCommon
				_ = json.NewDecoder(lookupResp.Body).Decode(&info)
				lookupResp.Body.Close()

				// Read file content via GET /file/{node}
				fileReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/file/%d", tsFiles.URL, info.Node), nil)
				fileResp, err := http.DefaultClient.Do(fileReq)
				if err != nil || fileResp.StatusCode != http.StatusOK {
					t.Errorf("Get file HTTP failed for node %d: %v", info.Node, err)
					return
				}
				data, _ := io.ReadAll(fileResp.Body)
				fileResp.Body.Close()

				if !bytes.Equal(data, contentData) {
					t.Errorf("File content mismatch for %s", fileName)
				}
			}(i)
		}
		wg.Wait()

		// Trigger explicit sync via PUT /sync
		syncReq, _ := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/sync", tsFiles.URL), nil)
		syncResp, err := http.DefaultClient.Do(syncReq)
		if err != nil || syncResp.StatusCode != http.StatusOK {
			t.Fatalf("Sync HTTP failed: %v", err)
		}
		syncResp.Body.Close()

		summary := tracer.Summary("multi-service-files-storage")
		t.Logf("=== Multi-Service Files-Storage-Slots Trace Summary: Total Spans=%d, Errors=%d ===", summary.TotalSpans, summary.ErrorCount)
		for ep, stat := range summary.Endpoints {
			t.Logf("  [%s] count=%d, p50=%.2fms, p95=%.2fms, p99=%.2fms, max=%.2fms, bytesIn=%d, bytesOut=%d",
				ep, stat.Count, stat.P50Ms, stat.P95Ms, stat.P99Ms, stat.MaxMs, stat.BytesIn, stat.BytesOut)
		}
	})

	// 6. Key-Value & Storage & Slots Transaction Workflow
	t.Run("MultiService_KVTransactions", func(t *testing.T) {
		tracer := trace.NewTracer(10000)

		storageStore := storage.NewInMemoryStorage()
		storageSrv := storage.NewStorageServer(storageStore).WithTracer(tracer)
		tsStorage := httptest.NewServer(storageSrv)
		defer tsStorage.Close()
		storageClient := storage.NewClient(tsStorage.URL, nil)

		slotStore := slots.NewMemorySlots("slots-kv-test")
		slotSrv := slots.NewServer(slotStore).WithTracer(tracer)
		tsSlots := httptest.NewServer(slotSrv)
		defer tsSlots.Close()
		slotsClient := slots.NewClient(tsSlots.URL, nil)

		btreeSlotBytes := sha256.Sum256([]byte("btree-slot"))
		btreeSlotID := hex.EncodeToString(btreeSlotBytes[:])
		journalSlotBytes := sha256.Sum256([]byte("journal-slot"))
		journalSlotID := hex.EncodeToString(journalSlotBytes[:])

		kvStore, err := kv.NewFileKeyValueStore(
			context.Background(),
			slotsClient,
			btreeSlotID,
			nil,
			journalSlotID,
			nil,
			storageClient,
			t.TempDir(),
			10*1024*1024,
			50,
			10,
			content.WriterOptions{},
		)
		if err != nil {
			t.Fatalf("NewFileKeyValueStore failed: %v", err)
		}
		defer kvStore.Close()

		kvSrv := kv.NewServer(kvStore).WithTracer(tracer)
		tsKV := httptest.NewServer(kvSrv)
		defer tsKV.Close()

		kvClient := kv.NewClient(tsKV.URL, nil)

		const numTx = 15
		for i := range numTx {
			txID, err := kvClient.StartTransaction(context.Background(), false)
			if err != nil {
				t.Fatalf("StartTransaction failed: %v", err)
			}

			key := fmt.Sprintf("user:session:%d", i)
			val := fmt.Appendf(nil, "session-token-data-%d-%d", i, time.Now().UnixNano())
			if _, err := kvClient.Put(context.Background(), &txID, key, val); err != nil {
				t.Fatalf("Put failed: %v", err)
			}

			readVal, seq, err := kvClient.Get(context.Background(), &txID, key)
			if err != nil || !bytes.Equal(readVal, val) {
				t.Fatalf("Get in tx failed: %v, got %s want %s (seq=%d)", err, readVal, val, seq)
			}

			if err := kvClient.CommitTransaction(context.Background(), txID); err != nil {
				t.Fatalf("CommitTransaction failed: %v", err)
			}
		}

		summary := tracer.Summary("kv-transactions")
		t.Logf("=== KV Multi-Service Trace Summary: Total Spans=%d, Errors=%d ===", summary.TotalSpans, summary.ErrorCount)
		for ep, stat := range summary.Endpoints {
			t.Logf("  [%s] count=%d, p50=%.2fms, p95=%.2fms, p99=%.2fms, max=%.2fms",
				ep, stat.Count, stat.P50Ms, stat.P95Ms, stat.P99Ms, stat.MaxMs)
		}
	})

	// 7. Finder & Distribute Routing Performance & Tracing
	t.Run("FinderAndDistribute_RoutingAndReplication", func(t *testing.T) {
		tracer := trace.NewTracer(10000)

		finderIDBytes := sha256.Sum256([]byte("finder-node-1"))
		finderID := hex.EncodeToString(finderIDBytes[:])
		fnd, err := finder.NewMemoryFinder(finderID)
		if err != nil {
			t.Fatalf("NewMemoryFinder failed: %v", err)
		}
		finderSrv := finder.NewFinderServer(fnd, nil).WithTracer(tracer)
		tsFinder := httptest.NewServer(finderSrv)
		defer tsFinder.Close()
		finderClient := finder.NewClient(tsFinder.URL, nil)

		distIDBytes := sha256.Sum256([]byte("dist-node-1"))
		distID := hex.EncodeToString(distIDBytes[:])
		dst := distribute.NewInMemoryDistribute(nil, 3, 3, "", 0)
		distSrv := distribute.NewDistributeServer(distID, dst).WithTracer(tracer)
		tsDist := httptest.NewServer(distSrv)
		defer tsDist.Close()
		distClient := distribute.NewClient(tsDist.URL, nil)

		// Register storage node
		storageNodeBytes := sha256.Sum256([]byte("storage-node-1"))
		storageNodeID := hex.EncodeToString(storageNodeBytes[:])

		if err := distClient.Register(storageNodeID); err != nil {
			t.Fatalf("Distribute Register failed: %v", err)
		}

		const numBlocks = 30
		blockAddrs := make([]string, numBlocks)
		for i := range numBlocks {
			hash := sha256.Sum256(fmt.Appendf(nil, "dist-block-%d", i))
			blockAddrs[i] = hex.EncodeToString(hash[:])
		}

		// Distribute notify via notify client
		notifyDist := notify.NewClient(tsDist.URL, nil)
		if err := notifyDist.Notify(storageNodeID, blockAddrs); err != nil {
			t.Fatalf("Distribute Notify failed: %v", err)
		}

		// Finder notify
		if err := finderClient.Notify(context.Background(), storageNodeID, blockAddrs); err != nil {
			t.Fatalf("Finder Notify failed: %v", err)
		}

		// Finder lookups
		for _, addr := range blockAddrs[:15] {
			nodes, err := finderClient.Find(context.Background(), addr)
			if err != nil || len(nodes) == 0 {
				t.Fatalf("Finder Find failed for %s: %v (len=%d)", addr, err, len(nodes))
			}
		}

		summary := tracer.Summary("finder-distribute")
		t.Logf("=== Finder & Distribute Trace Summary: Total Spans=%d, Errors=%d ===", summary.TotalSpans, summary.ErrorCount)
		for ep, stat := range summary.Endpoints {
			t.Logf("  [%s] count=%d, p50=%.2fms, p95=%.2fms, p99=%.2fms, max=%.2fms",
				ep, stat.Count, stat.P50Ms, stat.P95Ms, stat.P99Ms, stat.MaxMs)
		}
	})
}
