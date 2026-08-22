package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"

	"invariant/internal/content"
	"invariant/internal/discovery"
	"invariant/internal/finder"
	"invariant/internal/kv"
	"invariant/internal/slots"
	"invariant/internal/storage"
	"invariant/internal/tags"
	"invariant/internal/trace"
)

func generateID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func main() {
	var dir string
	flag.StringVar(&dir, "dir", "kv-journal", "Directory for local KV journal files")
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var advertiseAddr string
	flag.StringVar(&advertiseAddr, "advertise", "", "Address to advertise to the discovery service")
	tagFlags := tags.RegisterFlags()
	var port int
	flag.IntVar(&port, "port", 0, "Port to listen on (0 for random available port)")
	var name string
	flag.StringVar(&name, "name", "", "Name to register with the names service")

	var btreeSlotID string
	flag.StringVar(&btreeSlotID, "btree-slot-id", "", "Slot ID to use for the B-Tree root (32-byte hex). Randomly generated if not provided.")
	var btreeSlotAuthHex string
	flag.StringVar(&btreeSlotAuthHex, "btree-slot-auth", "", "Hex encoded auth signature for B-Tree slot updates (if required)")

	var journalSlotID string
	flag.StringVar(&journalSlotID, "journal-slot-id", "", "Slot ID to use for the Journal (32-byte hex). Randomly generated if not provided.")
	var journalSlotAuthHex string
	flag.StringVar(&journalSlotAuthHex, "journal-slot-auth", "", "Hex encoded auth signature for Journal slot updates (if required)")

	var btreeThreshold int
	flag.IntVar(&btreeThreshold, "btree-threshold", 1000, "Number of pending records before merging into B-Tree")
	var journalThreshold int
	flag.IntVar(&journalThreshold, "journal-threshold", 100, "Number of records before flushing journal to storage")
	var maxCacheSize int
	flag.IntVar(&maxCacheSize, "max-cache-size", 10*1024*1024, "Maximum cache size in bytes")

	var compress bool
	flag.BoolVar(&compress, "compress", false, "Compress the written content")
	var encrypt bool
	flag.BoolVar(&encrypt, "encrypt", false, "Encrypt the written content")
	var keyPolicyStr string
	flag.StringVar(&keyPolicyStr, "key-policy", "Deterministic", "Encryption key policy (RandomPerBlock, RandomAllKey, Deterministic, SuppliedAllKey)")
	var keyStr string
	flag.StringVar(&keyStr, "key", "", "32-byte hex-encoded key (required if key-policy is SuppliedAllKey)")
	var enableTrace bool
	flag.BoolVar(&enableTrace, "trace", false, "Enable distributed tracing on this service")

	flag.Parse()

	if btreeSlotID == "" {
		btreeSlotID = generateID()
	}
	if journalSlotID == "" {
		journalSlotID = generateID()
	}

	var btreeSlotAuth []byte
	if btreeSlotAuthHex != "" {
		var err error
		btreeSlotAuth, err = hex.DecodeString(btreeSlotAuthHex)
		if err != nil {
			log.Fatalf("Invalid btree-slot-auth hex: %v", err)
		}
	}

	var journalSlotAuth []byte
	if journalSlotAuthHex != "" {
		var err error
		journalSlotAuth, err = hex.DecodeString(journalSlotAuthHex)
		if err != nil {
			log.Fatalf("Invalid journal-slot-auth hex: %v", err)
		}
	}

	if discoveryURL == "" {
		log.Fatalf("A discovery service URL is required to find storage and slots services")
	}

	disc := discovery.NewClient(discoveryURL, nil)

	findService := func(kind string) string {
		id, err := disc.Find(context.Background(), kind, "", 1)
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

	// Create storage client
	storageClient := storage.NewAggregateClient(finderClient, disc, 3, 1000)

	slotsAddr := findService("slots-v1")

	// Create slots client
	slotsClient := slots.NewClient(slotsAddr, nil)

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

	store, err := kv.NewFileKeyValueStore(
		context.Background(),
		slotsClient,
		btreeSlotID,
		btreeSlotAuth,
		journalSlotID,
		journalSlotAuth,
		storageClient,
		dir,
		maxCacheSize,
		btreeThreshold,
		journalThreshold,
		writerOpts,
	)
	if err != nil {
		log.Fatalf("Failed to initialize KV store: %v", err)
	}
	defer store.Close()

	server := kv.NewServer(store)
	if enableTrace {
		server.WithTracer(trace.NewTracer(10000))
	}

	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", addr, err)
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port

	myID := server.ID()
	err = discovery.AdvertiseAndRegister(context.Background(), disc, myID, advertiseAddr, actualPort, []string{"kv-v1", "kv-batch-v1"}, tagFlags.Tags())
	if err != nil {
		log.Fatalf("Failed to register with discovery service: %v", err)
	}
	log.Printf("Registered with discovery service %s as %s", discoveryURL, myID)

	if name != "" {
		go func() {
			err := discovery.RegisterName(context.Background(), disc, name, myID, []string{"kv-v1", "kv-batch-v1"})
			if err != nil {
				log.Printf("Failed to register name %q: %v", name, err)
			} else {
				log.Printf("Registered name %q for ID %s", name, myID)
			}
		}()
	}

	log.Printf("KV service listening on :%d (BTree Slot ID: %s, Journal Slot ID: %s)...", actualPort, btreeSlotID, journalSlotID)
	log.Fatal(http.Serve(listener, server))
}
