package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

	mountRootDir, mountCfg, relPath, err := findMountRoot(dirPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	absTargetDir, _ := filepath.Abs(dirPath)

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

	discURL := mountCfg.DiscoveryURL
	if discURL == "" && globalCfg != nil {
		discURL = globalCfg.Discovery
	}

	targetSegments := splitPath(relPath)

	ranDirectTraversal := false
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
			cachingStore := storage.NewCachingStorageNoScan(l2Store, storageClient, 10*1024*1024*1024, 8*1024*1024*1024, true)

			rootAddr := mountCfg.RootAddr
			isSlot := mountCfg.Slot != ""
			if isSlot {
				resolved, err := discovery.ResolveName(context.Background(), dClient, mountCfg.Slot)
				if err == nil {
					rootAddr = resolved
				}
			}

			if rootAddr != "" {
				maxWorkers := max(32, min(128, runtime.NumCPU()*4))
				sem := make(chan struct{}, maxWorkers)
				rootLink := content.ContentLink{Address: rootAddr, Slot: isSlot}
				cacheContentTree(context.Background(), rootLink, l2Store, cachingStore, slotsClient, &fileCount, &totalBytes, sem, targetSegments, nil)
				ranDirectTraversal = true
			}
		}
	}

	if !ranDirectTraversal {
		buf := make([]byte, 64*1024)
		err = filepath.WalkDir(absTargetDir, func(path string, d os.DirEntry, err error) error {
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

		curr := absTargetDir
		for curr != mountRootDir {
			parent := filepath.Dir(curr)
			_, _ = os.ReadDir(parent)
			if parent == curr {
				break
			}
			curr = parent
		}
	}

	if relPath != "" {
		fmt.Printf("Successfully cached all blocks for %s (in mount %s) in %s (%d files, %d bytes)\n", absTargetDir, mountRootDir, cacheDir, fileCount, totalBytes)
	} else {
		fmt.Printf("Successfully cached all blocks for mount %s in %s (%d files, %d bytes)\n", mountRootDir, cacheDir, fileCount, totalBytes)
	}
}

func findMountRoot(dirPath string) (string, files.MountConfig, string, error) {
	absDir, err := filepath.Abs(dirPath)
	if err != nil {
		return "", files.MountConfig{}, "", fmt.Errorf("invalid directory path: %w", err)
	}

	curr := absDir
	for {
		mountJsonPath := filepath.Join(curr, ".invariant-mount.json")
		data, err := os.ReadFile(mountJsonPath)
		if err == nil {
			var cfg files.MountConfig
			if err := json.Unmarshal(data, &cfg); err == nil && cfg.InvariantMount {
				rel, err := filepath.Rel(curr, absDir)
				if err != nil || rel == "." {
					rel = ""
				}
				return curr, cfg, rel, nil
			}
		}

		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}

	return "", files.MountConfig{}, "", fmt.Errorf("error: %s is not an active invariant mount (missing .invariant-mount.json)", absDir)
}

func splitPath(p string) []string {
	p = filepath.Clean(p)
	if p == "." || p == "" || p == "/" {
		return nil
	}
	parts := strings.Split(p, string(filepath.Separator))
	var clean []string
	for _, part := range parts {
		if part != "" && part != "." {
			clean = append(clean, part)
		}
	}
	return clean
}

func hasPathPrefix(path, prefix []string) bool {
	if len(prefix) > len(path) {
		return false
	}
	for i := range prefix {
		if path[i] != prefix[i] {
			return false
		}
	}
	return true
}

func cacheContentTree(
	ctx context.Context,
	link content.ContentLink,
	l2Store storage.Storage,
	store storage.Storage,
	slotsClient slots.Slots,
	fileCount, totalBytes *uint64,
	sem chan struct{},
	targetSegments []string,
	currentSegments []string,
) {
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

	isParentOfTarget := len(targetSegments) > 0 && len(currentSegments) < len(targetSegments) && hasPathPrefix(targetSegments, currentSegments)
	isInsideOrEqualTarget := len(targetSegments) == 0 || (len(currentSegments) >= len(targetSegments) && hasPathPrefix(currentSegments, targetSegments))

	var nextTargetSegment string
	if isParentOfTarget {
		nextTargetSegment = targetSegments[len(currentSegments)]
	}

	var wg sync.WaitGroup
	for _, entry := range dir {
		entry := entry
		entryName := entry.GetName()

		childSegments := make([]string, len(currentSegments)+1)
		copy(childSegments, currentSegments)
		childSegments[len(currentSegments)] = entryName

		switch entry.GetKind() {
		case filetree.DirectoryKind:
			if de, ok := entry.(*filetree.DirectoryEntry); ok {
				shouldTraverse := false
				if isInsideOrEqualTarget {
					shouldTraverse = true
				} else if isParentOfTarget && entryName == nextTargetSegment {
					shouldTraverse = true
				}

				if shouldTraverse {
					wg.Add(1)
					sem <- struct{}{}
					go func(de *filetree.DirectoryEntry, childSegs []string) {
						defer func() {
							<-sem
							wg.Done()
						}()
						cacheContentTree(ctx, de.Content, l2Store, store, slotsClient, fileCount, totalBytes, sem, targetSegments, childSegs)
					}(de, childSegments)
				}
			}
		case filetree.FileKind:
			if fe, ok := entry.(*filetree.FileEntry); ok {
				if isInsideOrEqualTarget {
					wg.Add(1)
					sem <- struct{}{}
					go func(fe *filetree.FileEntry) {
						defer func() {
							<-sem
							wg.Done()
						}()
						ensureContentCached(ctx, fe.Content, l2Store, store, slotsClient, fileCount, totalBytes, sem)
					}(fe)
				}
			}
		}
	}
	wg.Wait()
}

func ensureContentCached(ctx context.Context, link content.ContentLink, l2Store storage.Storage, store storage.Storage, slotsClient slots.Slots, fileCount, totalBytes *uint64, sem chan struct{}) {
	address := link.Address
	if link.Slot && slotsClient != nil {
		if resolved, err := slotsClient.Get(ctx, link.Address); err == nil {
			address = resolved
		}
	}

	if address == "" {
		return
	}

	hasBlocksTransform := false
	for _, t := range link.Transforms {
		if t.Kind == "Blocks" {
			hasBlocksTransform = true
			break
		}
	}

	if fileCount != nil {
		atomic.AddUint64(fileCount, 1)
	}

	if l2Store != nil && l2Store.Has(ctx, address) && !hasBlocksTransform {
		if totalBytes != nil {
			if size, ok := l2Store.Size(ctx, address); ok {
				atomic.AddUint64(totalBytes, uint64(size))
			}
		}
		return
	}

	frc, err := content.Read(link, store, slotsClient)
	if err == nil {
		n, _ := io.Copy(io.Discard, frc)
		frc.Close()
		if totalBytes != nil {
			atomic.AddUint64(totalBytes, uint64(n))
		}
	}
}
