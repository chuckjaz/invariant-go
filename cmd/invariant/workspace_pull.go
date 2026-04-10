package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"invariant/internal/config"
	"invariant/internal/files"
	"invariant/internal/filetree"
	"invariant/internal/workspace"
)

func runWorkspacePull(globalCfg *config.InvariantConfig, args []string) {
	pullFlags := flag.NewFlagSet("workspace pull", flag.ExitOnError)
	var commonFlags CommonMountFlags
	commonFlags.Register(pullFlags)

	pullFlags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant workspace pull [directory] [options]\n")
		pullFlags.PrintDefaults()
	}
	pullFlags.Parse(args)

	directory := "."
	if pullFlags.NArg() > 0 {
		directory = pullFlags.Arg(0)
	}

	absDir, err := filepath.Abs(directory)
	if err != nil {
		log.Fatalf("invalid directory path: %v", err)
	}

	dClient, _, storageClient, slotsClient := initClients(globalCfg)
	finalStorage, localStore := SetupCacheStorage(&commonFlags, storageClient)

	var layers []files.Layer

	wsPath := filepath.Join(absDir, ".invariant-workspace")
	data, err := os.ReadFile(wsPath)
	if err == nil {
		var wsInfo workspace.WorkspaceInfo
		if err := json.Unmarshal(data, &wsInfo); err != nil {
			log.Fatalf("Invalid workspace file %s: %v", wsPath, err)
		}
		layers, err = workspace.ResolveLayers(context.Background(), slotsClient, finalStorage, wsInfo.Content)
		if err != nil {
			log.Fatalf("Failed to resolve layers: %v", err)
		}
	} else {
		layerData, layerErr := os.ReadFile(filepath.Join(absDir, ".invariant-layers"))
		if layerErr != nil {
			layerData, layerErr = os.ReadFile(filepath.Join(absDir, ".invariant-layer"))
		}
		if layerErr != nil {
			log.Fatalf("Could not find .invariant-workspace or .invariant-layer[s] in %s", absDir)
		}
		if err := json.Unmarshal(layerData, &layers); err != nil {
			log.Fatalf("Failed to parse .invariant-layer from %s: %v", absDir, err)
		}
	}

	var sourceLayer *files.Layer
	for i := len(layers) - 1; i >= 0; i-- {
		if layers[i].RootLink.Address != "" || layers[i].RootLink.Slot {
			if layers[i].StorageDestination != "local" {
				sourceLayer = &layers[i]
				break
			}
		}
	}

	if sourceLayer == nil {
		log.Fatalf("Could not identify source layer")
	}

	filesOpts := files.Options{
		Storage:      finalStorage,
		LocalStorage: localStore,
		Slots:        slotsClient,
		Discovery:    dClient,
		RootLink:     sourceLayer.RootLink,
		Layers:       []files.Layer{*sourceLayer},
	}

	fs, err := files.NewInMemoryFiles(filesOpts)
	if err != nil {
		log.Fatalf("Failed to load source layer file tree: %v", err)
	}
	defer fs.Close()

	var totalSize uint64
	var fileNodes []uint64
	var mu sync.Mutex
	var walkWg sync.WaitGroup

	sem := make(chan struct{}, 16)

	var walk func(nodeID uint64)
	walk = func(nodeID uint64) {
		defer walkWg.Done()
		sem <- struct{}{}
		defer func() { <-sem }()

		entries, err := fs.ReadDirectory(context.Background(), nodeID, 0, 0)
		if err != nil {
			log.Printf("Warning: failed to read dir %d: %v", nodeID, err)
			return
		}

		for _, entry := range entries {
			info, err := fs.Lookup(context.Background(), nodeID, entry.GetName())
			if err != nil {
				continue
			}
			if entry.GetKind() == filetree.DirectoryKind {
				walkWg.Add(1)
				go walk(info.Node)
			} else if entry.GetKind() == filetree.FileKind {
				if fe, ok := entry.(*filetree.FileEntry); ok {
					mu.Lock()
					totalSize += fe.Size
					fileNodes = append(fileNodes, info.Node)
					mu.Unlock()
				}
			}
		}
	}

	walkWg.Add(1)
	go walk(1)
	walkWg.Wait()

	requiredMB := int(totalSize / (1024 * 1024))
	if requiredMB == 0 && totalSize > 0 {
		requiredMB = 1
	}

	if requiredMB > commonFlags.DiskCacheSizeMB {
		fmt.Printf("The required cache size (~%d MB) exceeds the disk cache limit (%d MB).\n", requiredMB, commonFlags.DiskCacheSizeMB)
		fmt.Printf("Would you like to proceed using the overflow cache? [y/N]: ")
		var resp string
		fmt.Scanln(&resp)
		if resp != "y" && resp != "Y" {
			fmt.Println("Aborting workspace pull.")
			os.Exit(1)
		}
	}

	fmt.Printf("Pulling workspace content (~%d MB)...\n", requiredMB)

	var wg sync.WaitGroup
	var pulledBytes uint64

	concurrency := 16
	jobs := make(chan uint64, len(fileNodes))
	for _, n := range fileNodes {
		jobs <- n
	}
	close(jobs)

	for range concurrency {
		wg.Go(func() {
			for nodeID := range jobs {
				r, err := fs.ReadFile(context.Background(), nodeID, 0, 0)
				if err != nil {
					log.Printf("Warning: failed to read node %d: %v", nodeID, err)
					continue
				}
				n, err := io.Copy(io.Discard, r)
				r.Close()
				if err != nil && err != io.EOF {
					log.Printf("Warning: error reading node %d: %v", nodeID, err)
				}
				atomic.AddUint64(&pulledBytes, uint64(n))
			}
		})
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			fmt.Printf("\nWorkspace pull complete. Total %d MB downloaded into cache.\n", (pulledBytes+1024*1024-1)/(1024*1024))
			return
		case <-ticker.C:
			fmt.Printf("\rPulled %d MB / %d MB", atomic.LoadUint64(&pulledBytes)/(1024*1024), requiredMB)
		}
	}
}
