package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"invariant/internal/config"
	"invariant/internal/content"
	"invariant/internal/discovery"
	"invariant/internal/files"
	"invariant/internal/filetree"
	"invariant/internal/finder"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func runCache(globalCfg *config.InvariantConfig, args []string) {
	fsFlags := flag.NewFlagSet("cache", flag.ExitOnError)
	fsFlags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant cache [options] [mount_location]\n\n")
		fsFlags.PrintDefaults()
	}
	fsFlags.Parse(args)

	dirPath := "."
	if fsFlags.NArg() > 0 {
		dirPath = fsFlags.Arg(0)
	}

	absDir, err := filepath.Abs(dirPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid directory path: %v\n", err)
		os.Exit(1)
	}

	mountJsonPath := filepath.Join(absDir, ".invariant-mount.json")
	data, err := os.ReadFile(mountJsonPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s is not an active invariant mount (missing .invariant-mount.json)\n", absDir)
		os.Exit(1)
	}

	var mountCfg files.MountConfig
	if err := json.Unmarshal(data, &mountCfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading mount configuration from %s: %v\n", mountJsonPath, err)
		os.Exit(1)
	}

	if !mountCfg.InvariantMount {
		fmt.Fprintf(os.Stderr, "Error: %s is not a valid invariant mount\n", absDir)
		os.Exit(1)
	}

	cacheDir := mountCfg.CacheDir
	if cacheDir == "" {
		cacheDir, err = config.CacheDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to resolve cache directory: %v\n", err)
			os.Exit(1)
		}
	}

	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create cache directory %s: %v\n", cacheDir, err)
		os.Exit(1)
	}

	var fileCount uint64
	var totalBytes uint64
	buf := make([]byte, 64*1024)

	err = filepath.WalkDir(absDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if name == ".invariant-mount.json" || name == ".invariant-workspace" {
			return nil
		}

		if !d.IsDir() && d.Type().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			n, err := io.CopyBuffer(io.Discard, f, buf)
			f.Close()
			if err == nil {
				atomic.AddUint64(&fileCount, 1)
				atomic.AddUint64(&totalBytes, uint64(n))
			}
		}
		return nil
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning during mount directory traversal: %v\n", err)
	}

	discURL := mountCfg.DiscoveryURL
	if discURL == "" && globalCfg != nil {
		discURL = globalCfg.Discovery
	}

	if discURL != "" && (mountCfg.RootAddr != "" || mountCfg.Slot != "") {
		dClient := discovery.NewClient(discURL, nil)

		findService := func(kind string) string {
			id, err := dClient.Find(context.Background(), kind, 1)
			if err != nil || len(id) == 0 {
				return ""
			}
			return id[0].Address
		}

		finderAddr := findService("finder-v1")
		slotsAddr := findService("slots-v1")

		if finderAddr != "" {
			finderClient := finder.NewClient(finderAddr, nil)
			storageClient := storage.NewAggregateClient(finderClient, dClient, 3, 1000)

			var slotsClient slots.Slots
			if slotsAddr != "" {
				slotsClient = slots.NewClient(slotsAddr, nil)
			}

			l2Store := storage.NewFileSystemStorage(cacheDir)
			cachingStore := storage.NewCachingStorage(l2Store, storageClient, 10*1024*1024*1024, 8*1024*1024*1024, true)

			rootAddr := mountCfg.RootAddr
			isSlot := mountCfg.Slot != ""
			if isSlot {
				resolved, err := discovery.ResolveName(context.Background(), dClient, mountCfg.Slot)
				if err == nil {
					rootAddr = resolved
				}
			}

			if rootAddr != "" {
				rootLink := content.ContentLink{Address: rootAddr, Slot: isSlot}
				cacheContentTree(context.Background(), rootLink, cachingStore, slotsClient)
			}
		}
	}

	fmt.Printf("Successfully cached all blocks for mount %s in %s (%d files, %d bytes)\n", absDir, cacheDir, fileCount, totalBytes)
}

func cacheContentTree(ctx context.Context, link content.ContentLink, store storage.Storage, slotsClient slots.Slots) {
	rc, err := content.Read(link, store, slotsClient)
	if err != nil {
		return
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return
	}

	var dir filetree.Directory
	if err := json.Unmarshal(data, &dir); err != nil {
		return
	}

	for _, entry := range dir {
		switch entry.GetKind() {
		case filetree.DirectoryKind:
			if de, ok := entry.(*filetree.DirectoryEntry); ok {
				cacheContentTree(ctx, de.Content, store, slotsClient)
			}
		case filetree.FileKind:
			if fe, ok := entry.(*filetree.FileEntry); ok {
				frc, err := content.Read(fe.Content, store, slotsClient)
				if err == nil {
					io.Copy(io.Discard, frc)
					frc.Close()
				}
			}
		}
	}
}
