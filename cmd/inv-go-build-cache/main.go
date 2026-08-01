package main

import (
	"context"
	"encoding/hex"
	"flag"
	"log"
	"os"
	"path/filepath"

	"invariant/internal/buildcache"
	"invariant/internal/content"
	"invariant/internal/discovery"
	"invariant/internal/finder"
	"invariant/internal/kv"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func getEnvOrDefault(keys []string, defaultVal string) string {
	for _, key := range keys {
		if val := os.Getenv(key); val != "" {
			return val
		}
	}
	return defaultVal
}

func main() {
	var cacheDir string
	flag.StringVar(&cacheDir, "cache-dir", getEnvOrDefault([]string{"INVARIANT_BUILD_CACHE_DIR", "GOBUILDCACHE_DIR"}, ".invariant/build-cache"), "Directory for storing build cache files")

	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", getEnvOrDefault([]string{"INVARIANT_DISCOVERY", "DISCOVERY"}, ""), "URL of the discovery service")

	var kvURL string
	flag.StringVar(&kvURL, "kv", getEnvOrDefault([]string{"INVARIANT_KV", "KV_URL"}, ""), "URL of the KV service")

	var storageURL string
	flag.StringVar(&storageURL, "storage", getEnvOrDefault([]string{"INVARIANT_STORAGE", "STORAGE_URL"}, ""), "URL of the storage service")

	var slotsURL string
	flag.StringVar(&slotsURL, "slots", getEnvOrDefault([]string{"INVARIANT_SLOTS", "SLOTS_URL"}, ""), "URL of the slots service")

	var compress bool
	flag.BoolVar(&compress, "compress", false, "Compress written content")

	var compressAlgo string
	flag.StringVar(&compressAlgo, "compress-algo", getEnvOrDefault([]string{"INVARIANT_COMPRESS_ALGO"}, ""), "Compression algorithm (gzip, inflate, zstd)")

	var encrypt bool
	flag.BoolVar(&encrypt, "encrypt", false, "Encrypt written content")

	var encryptAlgo string
	flag.StringVar(&encryptAlgo, "encrypt-algo", getEnvOrDefault([]string{"INVARIANT_ENCRYPT_ALGO"}, ""), "Encryption algorithm (aes-256-cbc)")

	var keyPolicyStr string
	flag.StringVar(&keyPolicyStr, "key-policy", getEnvOrDefault([]string{"INVARIANT_KEY_POLICY"}, "Deterministic"), "Encryption key policy (RandomPerBlock, RandomAllKey, Deterministic, SuppliedAllKey)")

	var keyStr string
	flag.StringVar(&keyStr, "key", getEnvOrDefault([]string{"INVARIANT_KEY"}, ""), "32-byte hex-encoded key (required if key-policy is SuppliedAllKey)")

	flag.Parse()

	var disc discovery.Discovery
	if discoveryURL != "" {
		disc = discovery.NewClient(discoveryURL, nil)
	}

	findService := func(kind string) string {
		if disc == nil {
			return ""
		}
		id, err := disc.Find(context.Background(), kind, 1)
		if err != nil || len(id) == 0 {
			return ""
		}
		return id[0].Address
	}

	var kvStore kv.KeyValueStore
	if kvURL != "" {
		kvStore = kv.NewClient(kvURL, nil)
	} else if disc != nil {
		if addr := findService("kv-v1"); addr != "" {
			kvStore = kv.NewClient(addr, nil)
		}
	}

	var storageClient storage.Storage
	if storageURL != "" {
		storageClient = storage.NewClient(storageURL, nil)
	} else if disc != nil {
		if finderAddr := findService("finder-v1"); finderAddr != "" {
			finderClient := finder.NewClient(finderAddr, nil)
			storageClient = storage.NewAggregateClient(finderClient, disc, 3, 1000)
		}
	}

	var slotsClient slots.Slots
	if slotsURL != "" {
		slotsClient = slots.NewClient(slotsURL, nil)
	} else if disc != nil {
		if slotsAddr := findService("slots-v1"); slotsAddr != "" {
			slotsClient = slots.NewClient(slotsAddr, nil)
		}
	}

	// Standalone local fallbacks if services are not specified or discovered
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
