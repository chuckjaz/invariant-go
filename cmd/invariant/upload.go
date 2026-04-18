package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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

func parseIgnoreFile(path string) (*filetree.IgnoreMatcher, error) {
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
	return filetree.CompileIgnore(rules), nil
}

type trackingStorage struct {
	storage.Storage
	bytesUploaded *uint64
}

func (s *trackingStorage) Store(ctx context.Context, r io.Reader) (string, error) {
	tr := &trackingReader{r: r, size: s.bytesUploaded}
	return s.Storage.Store(ctx, tr)
}

func (s *trackingStorage) StoreAt(ctx context.Context, address string, r io.Reader) (bool, error) {
	tr := &trackingReader{r: r, size: s.bytesUploaded}
	return s.Storage.StoreAt(ctx, address, tr)
}

type trackingReader struct {
	r    io.Reader
	size *uint64
}

func (t *trackingReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		atomic.AddUint64(t.size, uint64(n))
	}
	return n, err
}

func (t *trackingReader) Close() error {
	if cl, ok := t.r.(io.Closer); ok {
		return cl.Close()
	}
	return nil
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
	var stats bool
	var dryRun bool
	fsFlags.BoolVar(&stats, "stats", false, "Emit total bytes to upload, number of blocks uploaded, number of directories created")
	fsFlags.BoolVar(&dryRun, "dry-run", false, "Compute stats but upload to a storage that reports having all blocks (dry-run)")
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
	var storageClient storage.Storage
	storageClient = storage.NewAggregateClient(finderClient, dClient, 3, 1000)

	if dryRun {
		storageClient = storage.NewDryRunStorage()
	}

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
			fmt.Fprintf(os.Stderr, "Error: RandomPerBlock is unsupported for file tree uploads because it incompatible with two-pass hashing.\n")
			os.Exit(1)
		case "RandomAllKey":
			opts.KeyPolicy = content.SuppliedAllKey
			k := make([]byte, 32)
			if _, err := io.ReadFull(rand.Reader, k); err != nil {
				fmt.Fprintf(os.Stderr, "Error generating RandomAllKey: %v\n", err)
				os.Exit(1)
			}
			opts.SuppliedKey = k
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
		fileQueue:    newWorkerQueue(10000, 100000),
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

	if stats || dryRun {
		bytesToUpload := atomic.LoadUint64(&up.BytesUploaded)
		blocksUploaded := atomic.LoadUint64(&up.BlocksUploaded)
		dirsCreated := atomic.LoadUint64(&up.DirsCreated)
		fmt.Fprintf(os.Stderr, "Stats: Total bytes to upload: %s, Blocks uploaded: %d, Directories created: %d\n", up.formatBytes(bytesToUpload), blocksUploaded, dirsCreated)
	}

	fmt.Printf("%s\n", out)
}

func (u *uploader) processDirectory(ctx context.Context, rootPath, currentPath string, store storage.Storage, rules *filetree.IgnoreMatcher, opts content.WriterOptions) (*filetree.DirectoryEntry, error) {
	entries, err := os.ReadDir(currentPath)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(currentPath)
	if err != nil {
		return nil, err
	}
	ctime, mtime := getEntryTimes(info)

	atomic.AddInt64(&u.DirsChecking, 1)
	defer atomic.AddInt64(&u.DirsChecking, -1)

	var wg sync.WaitGroup
	var dirErrs = make([]error, len(entries))
	var dirEntries = make([]filetree.Entry, len(entries))

	for i, d := range entries {
		wg.Add(1)

		task := func(idx int, d os.DirEntry) {
			defer wg.Done()

			relPath, _ := filepath.Rel(rootPath, filepath.Join(currentPath, d.Name()))

			if rules != nil && rules.Matches(relPath, d.IsDir()) {
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
		}

		if d.IsDir() {
			go task(i, d)
		} else {
			u.fileQueue.Submit(func() { task(i, d) })
		}
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

	memStore := storage.NewHashingStorage()
	memLink, err := content.Write(strings.NewReader(string(data)), memStore, opts)
	if err != nil {
		return nil, err
	}

	if !store.Has(ctx, memLink.Address) {
		atomic.AddInt64(&u.UploadsInFlight, 1)
		defer atomic.AddInt64(&u.UploadsInFlight, -1)

		atomic.AddUint64(&u.BlocksUploaded, 1)
		atomic.AddUint64(&u.DirsCreated, 1)

		trackingStore := &trackingStorage{
			Storage:       store,
			bytesUploaded: &u.BytesUploaded,
		}

		_, err = content.Write(strings.NewReader(string(data)), trackingStore, opts)
		if err != nil {
			return nil, err
		}
	} else {
		atomic.AddUint64(&u.DirsShared, 1)
	}

	atomic.AddUint64(&u.DirsChecked, 1)

	modeStr := fmt.Sprintf("%04o", info.Mode().Perm())

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

	atomic.AddInt64(&u.FilesChecking, 1)
	defer atomic.AddInt64(&u.FilesChecking, -1)

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

	memStore := storage.NewHashingStorage()
	memLink, err := content.Write(file, memStore, opts)
	if err != nil {
		return nil, err
	}

	if !store.Has(ctx, memLink.Address) {
		atomic.AddInt64(&u.UploadsInFlight, 1)
		defer atomic.AddInt64(&u.UploadsInFlight, -1)

		atomic.AddUint64(&u.BlocksUploaded, 1)

		// Rewind the open file descriptor to push natively without OOM allocations globally
		file.Seek(0, io.SeekStart)

		trackingStore := &trackingStorage{
			Storage:       store,
			bytesUploaded: &u.BytesUploaded,
		}

		_, err = content.Write(file, trackingStore, opts)
		if err != nil {
			return nil, err
		}
	} else {
		atomic.AddUint64(&u.FilesShared, 1)
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
