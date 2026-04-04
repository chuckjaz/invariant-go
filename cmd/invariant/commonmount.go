package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"invariant/internal/config"
	"invariant/internal/content"
	"invariant/internal/discovery"
	"invariant/internal/files"
	"invariant/internal/filetree"
	"invariant/internal/finder"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

type CommonMountFlags struct {
	DiscoveryURL    string
	RootAddr        string
	Slot            string
	CacheSizeMB     int
	DiskCacheSizeMB int
	CacheDir        string
	OverflowDir     string
	Compress        bool
	Encrypt         bool
	KeyPolicyStr    string
	KeyStr          string
}

func (f *CommonMountFlags) Register(fsFlags *flag.FlagSet) {
	fsFlags.StringVar(&f.DiscoveryURL, "discovery", "", "URL of the discovery service")
	fsFlags.StringVar(&f.RootAddr, "root", "", "Root block or slot address")
	fsFlags.StringVar(&f.Slot, "slot", "", "Whether the root address refers to a slot")
	fsFlags.IntVar(&f.CacheSizeMB, "cache", 128, "In-memory caching size in MB for storage backend (0 to disable)")
	fsFlags.IntVar(&f.DiskCacheSizeMB, "disk-cache", 1024, "Disk caching size in MB for storage backend (0 to disable)")
	fsFlags.StringVar(&f.CacheDir, "cache-dir", "", "Directory to use for the disk cache (default: ~/.cache/invariant)")
	fsFlags.StringVar(&f.OverflowDir, "overflow-dir", "", "Directory to use for the overflow cache (default: ~/.cache/invariant/overflow)")
	fsFlags.BoolVar(&f.Compress, "compress", false, "Compress the written content")
	fsFlags.BoolVar(&f.Encrypt, "encrypt", false, "Encrypt the written content")
	fsFlags.StringVar(&f.KeyPolicyStr, "key-policy", "Deterministic", "Encryption key policy (RandomPerBlock, RandomAllKey, Deterministic, SuppliedAllKey)")
	fsFlags.StringVar(&f.KeyStr, "key", "", "32-byte hex-encoded key (required if key-policy is SuppliedAllKey)")
}

func SetupCacheStorage(f *CommonMountFlags, baseStorage storage.Storage) (storage.Storage, storage.Storage) {
	finalStorage := baseStorage
	var directWrapper *storage.CachingStorage
	var localStore storage.Storage

	if f.DiskCacheSizeMB > 0 {
		if f.CacheDir == "" {
			cacheDir, err := config.CacheDir()
			if err != nil {
				log.Fatalf("Failed to get cache directory: %v", err)
			}
			f.CacheDir = cacheDir
		}

		if err := os.MkdirAll(f.CacheDir, 0700); err != nil {
			log.Fatalf("Failed to create cache directory: %v", err)
		}

		l2Store := storage.NewFileSystemStorage(f.CacheDir)
		maxSizeBytes := int64(f.DiskCacheSizeMB) * 1024 * 1024
		desiredSizeBytes := maxSizeBytes * 8 / 10
		cs := storage.NewCachingStorage(l2Store, finalStorage, maxSizeBytes, desiredSizeBytes, true)
		directWrapper = cs
		finalStorage = cs

		localStore = storage.NewCachingStorage(l2Store, nil, maxSizeBytes, desiredSizeBytes, true)
	}

	if f.CacheSizeMB > 0 {
		memStore := storage.NewInMemoryStorage()
		maxSizeBytes := int64(f.CacheSizeMB) * 1024 * 1024
		desiredSizeBytes := maxSizeBytes * 8 / 10
		cs := storage.NewCachingStorage(memStore, finalStorage, maxSizeBytes, desiredSizeBytes, true)
		if directWrapper == nil {
			directWrapper = cs
		}
		finalStorage = cs

		if localStore == nil {
			localStore = storage.NewCachingStorage(memStore, nil, maxSizeBytes, desiredSizeBytes, true)
		}
	}

	if localStore == nil {
		localStore = storage.NewInMemoryStorage()
	}

	if directWrapper != nil {
		if f.OverflowDir == "" {
			cacheDir, err := config.CacheDir()
			if err != nil {
				log.Fatalf("Failed to get cache directory for overflow: %v", err)
			}
			f.OverflowDir = filepath.Join(cacheDir, "overflow")
		}
		if err := os.MkdirAll(f.OverflowDir, 0700); err != nil {
			log.Fatalf("Failed to create overflow directory: %v", err)
		}
		overflowStore := storage.NewFileSystemStorage(f.OverflowDir)
		directWrapper.SetOverflow(overflowStore)
	}

	return finalStorage, localStore
}

func SetupFileSystem(globalCfg *config.InvariantConfig, f *CommonMountFlags) *files.InMemoryFiles {
	if f.DiscoveryURL == "" && globalCfg != nil {
		f.DiscoveryURL = globalCfg.Discovery
	}
	if f.DiscoveryURL == "" {
		log.Fatalf("Discovery URL is required")
	}

	var dClient discovery.Discovery
	dClient = discovery.NewClient(f.DiscoveryURL, nil)

	if f.RootAddr == "" && f.Slot == "" {
		log.Fatalf("Either --root or --slot is required")
	}

	rootIsSlot := false
	if f.Slot != "" {
		resolved, err := discovery.ResolveName(context.Background(), dClient, f.Slot)
		if err != nil {
			log.Fatalf("Could not resolve slot name: %v", err)
		}
		f.RootAddr = resolved
		rootIsSlot = true
	} else {
		resolved, err := discovery.ResolveName(context.Background(), dClient, f.RootAddr)
		if err != nil {
			log.Fatalf("Could not resolve root name: %v", err)
		}
		f.RootAddr = resolved
	}

	findService := func(kind string) string {
		id, err := dClient.Find(context.Background(), kind, 1)
		if err != nil {
			log.Fatalf("Could not find %s service: %v", kind, err)
		}
		if len(id) == 0 {
			log.Fatalf("Could not find %s service", kind)
		}
		return id[0].Address
	}

	finderAddr := findService("finder-v1")
	finderClient := finder.NewClient(finderAddr, nil)
	storageClient := storage.NewAggregateClient(finderClient, dClient, 3, 1000)
	slotsAddr := findService("slots-v1")
	slotsClient := slots.NewClient(slotsAddr, nil)

	finalStorage, localStore := SetupCacheStorage(f, storageClient)

	var writerOpts content.WriterOptions
	if f.Compress {
		writerOpts.CompressAlgorithm = "gzip"
	}
	if f.Encrypt {
		writerOpts.EncryptAlgorithm = "aes-256-cbc"

		switch f.KeyPolicyStr {
		case "RandomPerBlock":
			writerOpts.KeyPolicy = content.RandomPerBlock
		case "RandomAllKey":
			writerOpts.KeyPolicy = content.RandomAllKey
		case "Deterministic":
			writerOpts.KeyPolicy = content.Deterministic
		case "SuppliedAllKey":
			writerOpts.KeyPolicy = content.SuppliedAllKey
			if f.KeyStr == "" {
				log.Fatalf("Error: --key is required when --key-policy is SuppliedAllKey")
			}

			importHex, err := hex.DecodeString(f.KeyStr)
			if err != nil {
				log.Fatalf("Error parsing --key: %v", err)
			}
			if len(importHex) != 32 {
				log.Fatalf("Error: --key must be a 32-byte hex-encoded string (got %d bytes)", len(importHex))
			}
			writerOpts.SuppliedKey = importHex
		default:
			log.Fatalf("Error: unsupported key-policy '%s'", f.KeyPolicyStr)
		}
	}
	writerOpts.Splitters = []content.Splitter{
		&content.ZipSplitter{},
		&content.BuzHashSplitter{},
	}

	opts := files.Options{
		Storage:          finalStorage,
		LocalStorage:     localStore,
		Discovery:        dClient,
		Slots:            slotsClient,
		RootLink:         content.ContentLink{Address: f.RootAddr, Slot: rootIsSlot},
		AutoSyncTimeout:  time.Minute,
		SlotPollInterval: 5 * time.Minute,
		WriterOptions:    writerOpts,
	}

	rc, err := content.Read(opts.RootLink, finalStorage, slotsClient)
	if err != nil {
		log.Fatalf("Failed to resolve and read root directory: %v", err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		log.Fatalf("Failed to read root directory content: %v", err)
	}
	var dir filetree.Directory
	if err := json.Unmarshal(data, &dir); err != nil {
		log.Fatalf("Root directory content is not a valid directory format: %v", err)
	}

	var layers []files.Layer
	for _, entry := range dir {
		if entry.GetName() == ".invariant-layer" && entry.GetKind() == filetree.FileKind {
			if fe, ok := entry.(*filetree.FileEntry); ok {
				lrc, lerr := content.Read(fe.Content, finalStorage, slotsClient)
				if lerr == nil {
					ldata, err := io.ReadAll(lrc)
					lrc.Close()
					if err == nil {
						if err := json.Unmarshal(ldata, &layers); err != nil {
							log.Printf("Warning: failed to parse .invariant-layer: %v", err)
						}
					}
				}
			}
			break
		}
	}

	if len(layers) > 0 {
		opts.Layers = append(layers, files.Layer{
			RootLink: opts.RootLink,
		})
	}

	filesrv, err := files.NewInMemoryFiles(opts)
	if err != nil {
		log.Fatalf("Failed to initialize files service: %v", err)
	}

	return filesrv
}
