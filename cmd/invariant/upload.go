package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"invariant/internal/config"
	"invariant/internal/content"
	"invariant/internal/discovery"
	"invariant/internal/filetree"
	"invariant/internal/finder"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func parseIgnoreFile(path string) (filetree.IgnoreRules, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var rules []string
	lines := strings.SplitSeq(string(data), "\n")
	for line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rules = append(rules, line)
	}
	return rules, nil
}

func runUpload(globalCfg *config.InvariantConfig, args []string) {
	fsFlags := flag.NewFlagSet("upload", flag.ExitOnError)
	var discoveryURL string
	var slotID string
	var prevFlag string
	fsFlags.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	fsFlags.StringVar(&slotID, "slot", "", "Optional 32-byte hex Slot ID to update after successful upload")
	fsFlags.StringVar(&prevFlag, "prev", "", "Optional 32-byte hex previous Slot address (required if not locally cached)")
	var compress bool
	var encrypt bool
	var disableCache bool
	var keyPolicyStr string
	var keyStr string
	fsFlags.BoolVar(&compress, "compress", false, "Compress the uploaded content")
	fsFlags.BoolVar(&encrypt, "encrypt", false, "Encrypt the uploaded content")
	fsFlags.BoolVar(&disableCache, "no-cache", false, "Disable mtime caching")
	fsFlags.StringVar(&keyPolicyStr, "key-policy", "Deterministic", "Encryption key policy (RandomPerBlock, RandomAllKey, Deterministic, SuppliedAllKey)")
	fsFlags.StringVar(&keyStr, "key", "", "32-byte hex-encoded key (required if key-policy is SuppliedAllKey)")

	fsFlags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant upload [options] [directory]\n\n")
		fsFlags.PrintDefaults()
	}
	fsFlags.Parse(args)

	dirPath := "."
	if fsFlags.NArg() > 0 {
		dirPath = fsFlags.Arg(0)
	}

	if discoveryURL == "" && globalCfg != nil {
		discoveryURL = globalCfg.Discovery
	}
	if discoveryURL == "" {
		fmt.Fprintf(os.Stderr, "Discovery URL is required\n")
		os.Exit(1)
	}

	var dClient discovery.Discovery
	dClient = discovery.NewClient(discoveryURL, nil)

	findService := func(kind string) string {
		id, err := dClient.Find(context.Background(), kind, 1)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not find %s service: %v\n", kind, err)
			os.Exit(1)
		}
		if len(id) == 0 {
			fmt.Fprintf(os.Stderr, "Could not find %s service\n", kind)
			os.Exit(1)
		}
		return id[0].Address
	}

	finderAddr := findService("finder-v1")
	finderClient := finder.NewClient(finderAddr, nil)
	storageClient := storage.NewAggregateClient(finderClient, dClient, 3, 1000)

	var previousAddress string
	var privKeyHex []byte

	if slotID != "" {
		if len(slotID) != 64 {
			resolved, err := discovery.ResolveName(context.Background(), dClient, slotID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: --slot must be a 64-character hex string or valid registered name (resolution failed: %v)\n", err)
				os.Exit(1)
			}
			slotID = resolved
		}

		globalDir, err := config.ConfigDir()
		if err == nil {
			prevPath := filepath.Join(globalDir, "slots", fmt.Sprintf("%s.prev", slotID))
			if data, err := os.ReadFile(prevPath); err == nil {
				previousAddress = strings.TrimSpace(string(data))
			}
		}

		if previousAddress == "" {
			if prevFlag != "" {
				previousAddress = prevFlag
			} else {
				fmt.Fprintf(os.Stderr, "Error: previous slot address not found locally. Please provide it via --prev\n")
				os.Exit(1)
			}
		}

		keysDir, err := config.KeysDir()
		if err == nil {
			keyPath := filepath.Join(keysDir, fmt.Sprintf("%s.key", slotID))
			if data, err := os.ReadFile(keyPath); err == nil {
				privKeyHex = data
			}
		}
	}

	absPath, err := filepath.Abs(dirPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid directory path: %v\n", err)
		os.Exit(1)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot stat directory: %v\n", err)
		os.Exit(1)
	}
	if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "Target is not a directory: %v\n", absPath)
		os.Exit(1)
	}

	rules, err := parseIgnoreFile(filepath.Join(absPath, ".gitignore"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to read .gitignore: %v\n", err)
	}

	var opts content.WriterOptions
	if compress {
		opts.CompressAlgorithm = "gzip"
	}
	if encrypt {
		opts.EncryptAlgorithm = "aes-256-cbc"

		switch keyPolicyStr {
		case "RandomPerBlock":
			opts.KeyPolicy = content.RandomPerBlock
		case "RandomAllKey":
			opts.KeyPolicy = content.RandomAllKey
		case "Deterministic":
			opts.KeyPolicy = content.Deterministic
		case "SuppliedAllKey":
			opts.KeyPolicy = content.SuppliedAllKey
			if keyStr == "" {
				fmt.Fprintf(os.Stderr, "Error: --key is required when --key-policy is SuppliedAllKey\n")
				os.Exit(1)
			}

			importHex, err := hex.DecodeString(keyStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --key: %v\n", err)
				os.Exit(1)
			}
			if len(importHex) != 32 {
				fmt.Fprintf(os.Stderr, "Error: --key must be a 32-byte hex-encoded string (got %d bytes)\n", len(importHex))
				os.Exit(1)
			}
			opts.SuppliedKey = importHex
		default:
			fmt.Fprintf(os.Stderr, "Error: unsupported key-policy '%s'\n", keyPolicyStr)
			os.Exit(1)
		}
	}

	cacheDir := "/tmp"
	if cacheP, err := config.CacheDir(); err == nil {
		cacheDir = filepath.Join(cacheP, "upload")
	} else if globalDir, err := config.ConfigDir(); err == nil {
		cacheDir = filepath.Join(globalDir, "cache", "upload")
	}
	os.MkdirAll(cacheDir, 0755)
	cachePath := filepath.Join(cacheDir, "cache.json")

	up := &uploader{
		cache:        make(map[string]UploadCacheEntry),
		cachePath:    cachePath,
		disableCache: disableCache,
	}

	if !disableCache {
		data, err := os.ReadFile(cachePath)
		if err == nil {
			json.Unmarshal(data, &up.cache)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	go up.progressLoop(ctx)

	rootEntry, err := up.processDirectory(ctx, absPath, absPath, storageClient, rules, opts)
	cancel()

	if err != nil {
		fmt.Fprintf(os.Stderr, "Upload failed: %v\n", err)
		os.Exit(1)
	}

	if !disableCache {
		data, _ := json.Marshal(up.cache)
		os.WriteFile(cachePath, data, 0644)
	}

	out, err := json.MarshalIndent(rootEntry.Content, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal output: %v\n", err)
		os.Exit(1)
	}

	if slotID != "" {
		slotsAddr := findService("slots-v1")
		slotsClient := slots.NewClient(slotsAddr, nil)

		err = slotsClient.Update(context.Background(), slotID, rootEntry.Content.Address, previousAddress, privKeyHex)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to update slot: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Updated slot %s from %s to %s\n", slotID, previousAddress, rootEntry.Content.Address)

		globalDir, err := config.ConfigDir()
		if err == nil {
			slotsDir := filepath.Join(globalDir, "slots")
			os.MkdirAll(slotsDir, 0755)

			prevPath := filepath.Join(slotsDir, fmt.Sprintf("%s.prev", slotID))
			err = os.WriteFile(prevPath, []byte(previousAddress), 0644)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to write previous value to %s: %v\n", prevPath, err)
			}
		}
	}

	fmt.Printf("%s\n", out)
}

func (u *uploader) processDirectory(ctx context.Context, rootPath, currentPath string, store storage.Storage, rules filetree.IgnoreRules, opts content.WriterOptions) (*filetree.DirectoryEntry, error) {
	entries, err := os.ReadDir(currentPath)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(currentPath)
	if err != nil {
		return nil, err
	}
	ctime, mtime := getEntryTimes(info)

	var cacheKey string
	if !u.disableCache {
		cacheKey = currentPath
		u.cacheMu.RLock()
		ce, ok := u.cache[cacheKey]
		u.cacheMu.RUnlock()
		if ok && ce.MTime == *mtime {
			cl := content.ContentLink{}
			json.Unmarshal([]byte(ce.ContentLink), &cl)
			if store.Has(ctx, cl.Address) {
				atomic.AddUint64(&u.DirsSkipped, 1)
				modeStr := ce.Mode
				return &filetree.DirectoryEntry{
					BaseEntry: filetree.BaseEntry{
						Kind:       filetree.DirectoryKind,
						Name:       filepath.Base(currentPath),
						Mode:       &modeStr,
						CreateTime: ctime,
						ModifyTime: mtime,
					},
					Content: cl,
					Size:    ce.Size,
				}, nil
			}
		}
	}

	var wg sync.WaitGroup
	var dirErrs = make([]error, len(entries))
	var dirEntries = make([]filetree.Entry, len(entries))

	for i, d := range entries {
		wg.Add(1)
		go func(idx int, d os.DirEntry) {
			defer wg.Done()
			relPath, _ := filepath.Rel(rootPath, filepath.Join(currentPath, d.Name()))

			if rules.Matches(relPath, d.IsDir()) {
				return
			}

			if d.IsDir() {
				subDirEntry, err := u.processDirectory(ctx, rootPath, filepath.Join(currentPath, d.Name()), store, rules, opts)
				dirErrs[idx] = err
				if subDirEntry != nil {
					dirEntries[idx] = subDirEntry
				}
			} else if d.Type()&os.ModeSymlink != 0 {
				target, err := os.Readlink(filepath.Join(currentPath, d.Name()))
				if err != nil {
					dirErrs[idx] = err
					return
				}
				info, err := d.Info()
				if err != nil {
					dirErrs[idx] = err
					return
				}
				symCtime, symMtime := getEntryTimes(info)
				symlinkEntry := &filetree.SymbolicLinkEntry{
					BaseEntry: filetree.BaseEntry{
						Kind:       filetree.SymbolicLinkKind,
						Name:       d.Name(),
						CreateTime: symCtime,
						ModifyTime: symMtime,
					},
					Target: target,
				}
				dirEntries[idx] = symlinkEntry
			} else if d.Type().IsRegular() {
				fileEntry, err := u.processFile(ctx, filepath.Join(currentPath, d.Name()), d.Name(), store, opts)
				dirErrs[idx] = err
				if fileEntry != nil {
					dirEntries[idx] = fileEntry
				}
			}
		}(i, d)
	}

	wg.Wait()

	for _, err := range dirErrs {
		if err != nil {
			return nil, err
		}
	}

	var dir filetree.Directory
	for _, entry := range dirEntries {
		if entry != nil {
			dir = append(dir, entry)
		}
	}

	data, err := json.Marshal(dir)
	if err != nil {
		return nil, err
	}

	memStore := storage.NewInMemoryStorage()
	memLink, err := content.Write(strings.NewReader(string(data)), memStore, opts)
	if err != nil {
		return nil, err
	}

	if !store.Has(ctx, memLink.Address) {
		atomic.AddInt64(&u.UploadsInFlight, 1)
		defer atomic.AddInt64(&u.UploadsInFlight, -1)

		for batch := range memStore.List(ctx, 100) {
			for _, addr := range batch {
				if !store.Has(ctx, addr) {
					rc, ok := memStore.Get(ctx, addr)
					if ok {
						store.StoreAt(ctx, addr, rc)
						sz, _ := memStore.Size(ctx, addr)
						atomic.AddUint64(&u.BytesUploaded, uint64(sz))
						rc.Close()
					}
				}
			}
		}
	}

	atomic.AddUint64(&u.DirsChecked, 1)

	modeStr := fmt.Sprintf("%04o", info.Mode().Perm())
	if !u.disableCache {
		clBytes, _ := json.Marshal(memLink)
		u.cacheMu.Lock()
		u.cache[cacheKey] = UploadCacheEntry{
			MTime:       *mtime,
			ContentLink: string(clBytes),
			Size:        uint64(len(data)),
			Mode:        modeStr,
		}
		u.cacheMu.Unlock()
	}

	return &filetree.DirectoryEntry{
		BaseEntry: filetree.BaseEntry{
			Kind:       filetree.DirectoryKind,
			Name:       filepath.Base(currentPath),
			Mode:       &modeStr,
			CreateTime: ctime,
			ModifyTime: mtime,
		},
		Content: memLink,
		Size:    uint64(len(data)),
	}, nil
}

func (u *uploader) processFile(ctx context.Context, filePath, name string, store storage.Storage, opts content.WriterOptions) (*filetree.FileEntry, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return nil, err
	}

	ctime, mtime := getEntryTimes(fileInfo)

	var cacheKey string
	if !u.disableCache {
		cacheKey = filePath
		u.cacheMu.RLock()
		ce, ok := u.cache[cacheKey]
		u.cacheMu.RUnlock()
		if ok && ce.MTime == *mtime {
			cl := content.ContentLink{}
			json.Unmarshal([]byte(ce.ContentLink), &cl)
			if store.Has(ctx, cl.Address) {
				atomic.AddUint64(&u.FilesSkipped, 1)
				modeStr := ce.Mode
				return &filetree.FileEntry{
					BaseEntry: filetree.BaseEntry{
						Kind:       filetree.FileKind,
						Name:       name,
						Mode:       &modeStr,
						CreateTime: ctime,
						ModifyTime: mtime,
					},
					Content: cl,
					Size:    ce.Size,
				}, nil
			}
		}
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	opts.Filename = name
	opts.Splitters = []content.Splitter{
		&content.ZipSplitter{},
		&content.BuzHashSplitter{},
	}

	memStore := storage.NewInMemoryStorage()
	memLink, err := content.Write(file, memStore, opts)
	if err != nil {
		return nil, err
	}

	if !store.Has(ctx, memLink.Address) {
		atomic.AddInt64(&u.UploadsInFlight, 1)
		defer atomic.AddInt64(&u.UploadsInFlight, -1)

		for batch := range memStore.List(ctx, 100) {
			for _, addr := range batch {
				if !store.Has(ctx, addr) {
					rc, ok := memStore.Get(ctx, addr)
					if ok {
						store.StoreAt(ctx, addr, rc)
						sz, _ := memStore.Size(ctx, addr)
						atomic.AddUint64(&u.BytesUploaded, uint64(sz))
						rc.Close()
					}
				}
			}
		}
	}

	atomic.AddUint64(&u.FilesChecked, 1)

	modeStr := fmt.Sprintf("%04o", fileInfo.Mode().Perm())
	if !u.disableCache {
		clBytes, _ := json.Marshal(memLink)
		u.cacheMu.Lock()
		u.cache[cacheKey] = UploadCacheEntry{
			MTime:       *mtime,
			ContentLink: string(clBytes),
			Size:        uint64(fileInfo.Size()),
			Mode:        modeStr,
		}
		u.cacheMu.Unlock()
	}

	return &filetree.FileEntry{
		BaseEntry: filetree.BaseEntry{
			Kind:       filetree.FileKind,
			Name:       name,
			Mode:       &modeStr,
			CreateTime: ctime,
			ModifyTime: mtime,
		},
		Content: memLink,
		Size:    uint64(fileInfo.Size()),
	}, nil
}
