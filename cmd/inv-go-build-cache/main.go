package main

import (
	"context"
	"encoding/hex"
	"flag"
	"log"
	"os"
	"path/filepath"

	"invariant/internal/buildcache"
	"invariant/internal/config"
	"invariant/internal/content"
	"invariant/internal/discovery"
	"invariant/internal/finder"
	"invariant/internal/kv"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func main() {
	var cacheDir string
	flag.StringVar(&cacheDir, "cache-dir", ".invariant/build-cache", "Directory for storing build cache files")

	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service (overrides configuration)")

	var compress bool
	flag.BoolVar(&compress, "compress", false, "Compress written content")

	var compressAlgo string
	flag.StringVar(&compressAlgo, "compress-algo", "", "Compression algorithm (gzip, inflate, zstd)")

	var encrypt bool
	flag.BoolVar(&encrypt, "encrypt", false, "Encrypt written content")

	var encryptAlgo string
	flag.StringVar(&encryptAlgo, "encrypt-algo", "", "Encryption algorithm (aes-256-cbc)")

	var keyPolicyStr string
	flag.StringVar(&keyPolicyStr, "key-policy", "Deterministic", "Encryption key policy (RandomPerBlock, RandomAllKey, Deterministic, SuppliedAllKey)")

	var keyStr string
	flag.StringVar(&keyStr, "key", "", "32-byte hex-encoded key (required if key-policy is SuppliedAllKey)")

	flag.Parse()

	invCfg, err := config.Load()
	if err != nil {
		log.Fatalf("Error loading invariant configuration: %v", err)
	}

	discURL := discoveryURL
	if discURL == "" && invCfg != nil {
		discURL = invCfg.Discovery
	}

	var kvStore kv.KeyValueStore
	var storageClient storage.Storage
	var slotsClient slots.Slots

	if discURL != "" {
		disc := discovery.NewClient(discURL, nil)
		ctx := context.Background()

		findService := func(kind string) string {
			descs, err := disc.Find(ctx, kind, 1)
			if err != nil || len(descs) == 0 {
				return ""
			}
			return descs[0].Address
		}

		if kvAddr := findService("kv-v1"); kvAddr != "" {
			kvStore = kv.NewClient(kvAddr, nil)
		}

		if finderAddr := findService("finder-v1"); finderAddr != "" {
			finderClient := finder.NewClient(finderAddr, nil)
			storageClient = storage.NewAggregateClient(finderClient, disc, 3, 1000)
		}

		if slotsAddr := findService("slots-v1"); slotsAddr != "" {
			slotsClient = slots.NewClient(slotsAddr, nil)
		}
	}

	// Standalone local fallbacks if services are not provided or discovered
	if storageClient == nil {
		localStorageDir := filepath.Join(cacheDir, "storage")
		_ = os.MkdirAll(localStorageDir, 0755)
		storageClient = storage.NewFileSystemStorage(localStorageDir)
	}
	if kvStore == nil {
		kvStore = kv.NewMemoryKeyValueStore()
	}

	var writerOpts content.WriterOptions
	if compress || compressAlgo != "" {
		if compressAlgo == "" {
			compressAlgo = "gzip"
		}
		writerOpts.CompressAlgorithm = compressAlgo
	}

	if encrypt || encryptAlgo != "" {
		if encryptAlgo == "" {
			encryptAlgo = "aes-256-cbc"
		}
		writerOpts.EncryptAlgorithm = encryptAlgo

		switch keyPolicyStr {
		case "RandomPerBlock":
			writerOpts.KeyPolicy = content.RandomPerBlock
		case "RandomAllKey":
			writerOpts.KeyPolicy = content.RandomAllKey
		case "Deterministic", "":
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

	cfg := buildcache.CacheConfig{
		CacheDir:      cacheDir,
		KVStore:       kvStore,
		Storage:       storageClient,
		Slots:         slotsClient,
		WriterOptions: writerOpts,
	}

	handler, err := buildcache.NewHandler(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize build cache handler: %v", err)
	}

	if err := handler.Start(os.Stdin, os.Stdout); err != nil {
		log.Fatalf("Build cache handler exited with error: %v", err)
	}
}
