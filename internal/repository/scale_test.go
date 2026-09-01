package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sync"
	"testing"
	"time"

	"invariant/internal/content"
	"invariant/internal/files"
	"invariant/internal/filetree"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// StatisticalSummary captures latency distribution metrics.
type StatisticalSummary struct {
	Count  int           `json:"count"`
	Mean   time.Duration `json:"mean"`
	P50    time.Duration `json:"p50"`
	P90    time.Duration `json:"p90"`
	P95    time.Duration `json:"p95"`
	P99    time.Duration `json:"p99"`
	Min    time.Duration `json:"min"`
	Max    time.Duration `json:"max"`
	StdDev time.Duration `json:"stddev"`
}

func computePercentiles(durations []time.Duration) StatisticalSummary {
	if len(durations) == 0 {
		return StatisticalSummary{}
	}
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	slices.Sort(sorted)

	var total time.Duration
	for _, d := range sorted {
		total += d
	}
	mean := total / time.Duration(len(sorted))

	var sumSquares float64
	for _, d := range sorted {
		diff := float64(d - mean)
		sumSquares += diff * diff
	}
	stdDev := time.Duration(math.Sqrt(sumSquares / float64(len(sorted))))

	pIndex := func(p float64) time.Duration {
		idx := int(float64(len(sorted)-1) * p)
		return sorted[idx]
	}

	return StatisticalSummary{
		Count:  len(sorted),
		Mean:   mean,
		P50:    pIndex(0.50),
		P90:    pIndex(0.90),
		P95:    pIndex(0.95),
		P99:    pIndex(0.99),
		Min:    sorted[0],
		Max:    sorted[len(sorted)-1],
		StdDev: stdDev,
	}
}

func TestScale_100xRepository_StatsAndTreeFlattening(t *testing.T) {
	ctx := context.Background()
	store := storage.NewInMemoryStorage()
	slotsClient := slots.NewMemorySlots("scale-slots")

	const totalDirs = 100
	const filesPerDir = 500
	const totalFiles = totalDirs * filesPerDir // 50,000 files

	t.Logf("Generating 100x Scale Tree: %d directories, %d files per dir (%d total files)...", totalDirs, filesPerDir, totalFiles)

	genStart := time.Now()
	var rootDir filetree.Directory

	for d := range totalDirs {
		dirName := fmt.Sprintf("pkg_%03d", d)
		var subDir filetree.Directory

		for f := range filesPerDir {
			fileName := fmt.Sprintf("source_%04d.go", f)
			payload := fmt.Appendf(nil, "package %s\n// File %d in pkg %d\nfunc Func%04d() int { return %d }\n", dirName, f, d, f, f)

			cLink, _ := content.Write(bytes.NewReader(payload), store, content.WriterOptions{})
			subDir = append(subDir, &filetree.FileEntry{
				BaseEntry: filetree.BaseEntry{
					Name: fileName,
					Kind: filetree.FileKind,
				},
				Content: cLink,
				Size:    uint64(len(payload)),
			})
		}

		subDirJSON, _ := json.Marshal(subDir)
		subDirLink, _ := content.Write(bytes.NewReader(subDirJSON), store, content.WriterOptions{})

		rootDir = append(rootDir, &filetree.DirectoryEntry{
			BaseEntry: filetree.BaseEntry{
				Name: dirName,
				Kind: filetree.DirectoryKind,
			},
			Content: subDirLink,
		})
	}

	rootDirJSON, _ := json.Marshal(rootDir)
	rootLink, _ := content.Write(bytes.NewReader(rootDirJSON), store, content.WriterOptions{})
	t.Logf("Generated 50,000-file synthetic repository tree in %v (Root CAS: %s)", time.Since(genStart), rootLink.Address[:16])

	// 1. Benchmark FlattenFileTree across 50,000 files
	t.Log("=== Benchmark 1: Full 50,000-File Tree Flattening ===")
	flattenStart := time.Now()
	flatEntries, err := commit.FlattenFileTree(ctx, rootLink.Address, store, slotsClient)
	flattenDur := time.Since(flattenStart)
	if err != nil {
		t.Fatalf("FlattenFileTree failed: %v", err)
	}
	if len(flatEntries) != totalFiles {
		t.Fatalf("Expected %d flat entries, got %d", totalFiles, len(flatEntries))
	}
	t.Logf("  FlattenFileTree (50,000 files): %v (%.2f files/sec)", flattenDur, float64(totalFiles)/flattenDur.Seconds())

	// 2. Benchmark Parallel Stat & Inode Resolution in InMemoryFiles
	t.Log("=== Benchmark 2: Concurrent Multi-Threaded Node Lookups & Attribute Resolution ===")
	filesService, err := files.NewInMemoryFiles(files.Options{
		Storage:  store,
		RootLink: rootLink,
	})
	if err != nil {
		t.Fatalf("NewInMemoryFiles failed: %v", err)
	}
	defer filesService.Close()

	// Preload root directory and package directories to warm in-memory tree
	rootDirEntries, err := filesService.ReadDirectory(ctx, 1, 0, 0)
	if err != nil {
		t.Fatalf("ReadDirectory on root failed: %v", err)
	}
	for _, dirEntry := range rootDirEntries {
		if dirEntry.GetKind() == filetree.DirectoryKind {
			info, err := filesService.LookupNodeInfo(ctx, 1, dirEntry.GetName())
			if err == nil {
				_, _ = filesService.ReadDirectory(ctx, info.Node, 0, 0)
			}
		}
	}

	// Launch 16 worker goroutines performing lookups concurrently
	const numWorkers = 16
	const lookupsPerWorker = 1000
	var wg sync.WaitGroup
	durations := make([][]time.Duration, numWorkers)

	parStart := time.Now()
	for w := range numWorkers {
		durations[w] = make([]time.Duration, lookupsPerWorker)
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := range lookupsPerWorker {
				targetDirIdx := (workerID*1000 + i) % totalDirs
				targetFileIdx := (i * 7) % filesPerDir
				dirName := fmt.Sprintf("pkg_%03d", targetDirIdx)
				fileName := fmt.Sprintf("source_%04d.go", targetFileIdx)

				lookupStart := time.Now()
				// 1. Lookup directory node
				dirInfo, err := filesService.LookupNodeInfo(ctx, 1, dirName)
				if err == nil {
					// 2. Lookup file node within directory
					_, _ = filesService.LookupNodeInfo(ctx, dirInfo.Node, fileName)
				}
				durations[workerID][i] = time.Since(lookupStart)
			}
		}(w)
	}
	wg.Wait()
	totalParDur := time.Since(parStart)

	var allDurations []time.Duration
	for _, workerDurs := range durations {
		allDurations = append(allDurations, workerDurs...)
	}

	stats := computePercentiles(allDurations)
	t.Logf("  Total Lookups: %d across %d workers in %v (Throughput: %.2f lookups/sec)",
		len(allDurations), numWorkers, totalParDur, float64(len(allDurations))/totalParDur.Seconds())
	t.Logf("  Lookup Latency Distribution: Mean=%v, P50=%v, P90=%v, P95=%v, P99=%v, Min=%v, Max=%v",
		stats.Mean, stats.P50, stats.P90, stats.P95, stats.P99, stats.Min, stats.Max)

	if stats.P95 > time.Millisecond {
		t.Errorf("P95 lookup latency too high: %v (expected < 1ms)", stats.P95)
	}
	if stats.P99 > 50*time.Millisecond {
		t.Errorf("P99 lookup latency too high: %v (expected < 50ms)", stats.P99)
	}
}
