package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"

	"invariant/internal/config"
	"invariant/internal/content"
	"invariant/internal/discovery"
	"invariant/internal/files"
	"invariant/internal/filetree"
	"invariant/internal/finder"
	"invariant/internal/fuse"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func runMount(globalCfg *config.InvariantConfig, args []string) {
	fsFlags := flag.NewFlagSet("mount", flag.ExitOnError)
	var mountpoint string
	fsFlags.StringVar(&mountpoint, "mount", "", "Directory to mount the FUSE file system")
	var discoveryURL string
	fsFlags.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var rootAddr string
	fsFlags.StringVar(&rootAddr, "root", "", "Root block or slot address")
	var slot string
	fsFlags.StringVar(&slot, "slot", "", "Whether the root address refers to a slot")
	var cacheSizeMB int
	fsFlags.IntVar(&cacheSizeMB, "cache", 128, "In-memory caching size in MB for storage backend (0 to disable)")
	var diskCacheSizeMB int
	fsFlags.IntVar(&diskCacheSizeMB, "disk-cache", 1024, "Disk caching size in MB for storage backend (0 to disable)")
	var cacheDir string
	fsFlags.StringVar(&cacheDir, "cache-dir", "", "Directory to use for the disk cache (default: ~/.invariant/cache)")
	var compress bool
	var encrypt bool
	var keyPolicyStr string
	var keyStr string
	fsFlags.BoolVar(&compress, "compress", false, "Compress the written content")
	fsFlags.BoolVar(&encrypt, "encrypt", false, "Encrypt the written content")
	fsFlags.StringVar(&keyPolicyStr, "key-policy", "Deterministic", "Encryption key policy (RandomPerBlock, RandomAllKey, Deterministic, SuppliedAllKey)")
	fsFlags.StringVar(&keyStr, "key", "", "32-byte hex-encoded key (required if key-policy is SuppliedAllKey)")

	fsFlags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant mount [options]\n\n")
		fsFlags.PrintDefaults()
	}
	fsFlags.Parse(args)

	if mountpoint == "" {
		log.Fatalf("Mountpoint is required (--mount)")
	}

	if discoveryURL == "" && globalCfg != nil {
		discoveryURL = globalCfg.Discovery
	}
	if discoveryURL == "" {
		log.Fatalf("Discovery URL is required")
	}

	var dClient discovery.Discovery
	dClient = discovery.NewClient(discoveryURL, nil)

	if rootAddr == "" && slot == "" {
		log.Fatalf("Either --root or --slot is required")
	}

	rootIsSlot := false
	if slot != "" {
		resolved, err := discovery.ResolveName(context.Background(), dClient, slot)
		if err != nil {
			log.Fatalf("Could not resolve slot name: %v", err)
		}
		rootAddr = resolved
		rootIsSlot = true
	} else {
		resolved, err := discovery.ResolveName(context.Background(), dClient, rootAddr)
		if err != nil {
			log.Fatalf("Could not resolve root name: %v", err)
		}
		rootAddr = resolved
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

	var finalStorage storage.Storage = storageClient

	if diskCacheSizeMB > 0 {
		if cacheDir == "" {
			configDir, err := config.ConfigDir()
			if err != nil {
				log.Fatalf("Failed to get config directory for cache: %v", err)
			}
			cacheDir = filepath.Join(configDir, "cache")
		}

		if err := os.MkdirAll(cacheDir, 0700); err != nil {
			log.Fatalf("Failed to create cache directory: %v", err)
		}

		l2Store := storage.NewFileSystemStorage(cacheDir)
		maxSizeBytes := int64(diskCacheSizeMB) * 1024 * 1024
		desiredSizeBytes := maxSizeBytes * 8 / 10
		finalStorage = storage.NewCachingStorage(l2Store, finalStorage, maxSizeBytes, desiredSizeBytes, true)
	}

	if cacheSizeMB > 0 {
		localStore := storage.NewInMemoryStorage()
		maxSizeBytes := int64(cacheSizeMB) * 1024 * 1024
		desiredSizeBytes := maxSizeBytes * 8 / 10
		finalStorage = storage.NewCachingStorage(localStore, finalStorage, maxSizeBytes, desiredSizeBytes, true)
	}

	var writerOpts content.WriterOptions
	if compress {
		writerOpts.CompressAlgorithm = "gzip"
	}
	if encrypt {
		writerOpts.EncryptAlgorithm = "aes-256-cbc"

		switch keyPolicyStr {
		case "RandomPerBlock":
			writerOpts.KeyPolicy = content.RandomPerBlock
		case "RandomAllKey":
			writerOpts.KeyPolicy = content.RandomAllKey
		case "Deterministic":
			writerOpts.KeyPolicy = content.Deterministic
		case "SuppliedAllKey":
			writerOpts.KeyPolicy = content.SuppliedAllKey
			if keyStr == "" {
				log.Fatalf("Error: --key is required when --key-policy is SuppliedAllKey")
			}

			importHex, err := hex.DecodeString(keyStr)
			if err != nil {
				log.Fatalf("Error parsing --key: %v", err)
			}
			if len(importHex) != 32 {
				log.Fatalf("Error: --key must be a 32-byte hex-encoded string (got %d bytes)", len(importHex))
			}
			writerOpts.SuppliedKey = importHex
		default:
			log.Fatalf("Error: unsupported key-policy '%s'", keyPolicyStr)
		}
	}
	writerOpts.Splitters = []content.Splitter{
		&content.ZipSplitter{},
		&content.BuzHashSplitter{},
	}

	opts := files.Options{
		Storage:   finalStorage,
		Discovery: dClient,
		Slots:     slotsClient,
		RootLink: content.ContentLink{
			Address: rootAddr,
			Slot:    rootIsSlot,
		},
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
	defer filesrv.Close()

	rootNode := fuse.NewNode(filesrv, 1) // 1 is the root node ID in InMemoryFiles

	var uid, gid uint32
	if currentUser, err := user.Current(); err == nil {
		if parsedUID, err := strconv.ParseUint(currentUser.Uid, 10, 32); err == nil {
			uid = uint32(parsedUID)
		}
		if parsedGID, err := strconv.ParseUint(currentUser.Gid, 10, 32); err == nil {
			gid = uint32(parsedGID)
		}
	}

	server, err := fs.Mount(mountpoint, rootNode, &fs.Options{
		UID: uid,
		GID: gid,
	})
	if err != nil {
		log.Fatalf("Mount fail: %v\n", err)
	}

	log.Printf("Mounted on %s\n", mountpoint)
	log.Printf("Unmount by calling 'fusermount -u %s'", mountpoint)
	server.Wait()
}
