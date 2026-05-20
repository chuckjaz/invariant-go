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

	"invariant/internal/discovery"
	"invariant/internal/finder"
	"invariant/internal/kv"
	"invariant/internal/slots"
	"invariant/internal/storage"
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
	var port int
	flag.IntVar(&port, "port", 0, "Port to listen on (0 for random available port)")
	var name string
	flag.StringVar(&name, "name", "", "Name to register with the names service")

	var slotID string
	flag.StringVar(&slotID, "slot-id", "", "Slot ID to use for the B-Tree root (32-byte hex). Randomly generated if not provided.")
	var slotAuthHex string
	flag.StringVar(&slotAuthHex, "slot-auth", "", "Hex encoded auth signature for slot updates (if required)")

	var btreeThreshold int
	flag.IntVar(&btreeThreshold, "btree-threshold", 1000, "Number of pending records before merging into B-Tree")
	var journalThreshold int
	flag.IntVar(&journalThreshold, "journal-threshold", 100, "Number of records before flushing journal to storage")
	var maxCacheSize int
	flag.IntVar(&maxCacheSize, "max-cache-size", 10*1024*1024, "Maximum cache size in bytes")
	var finderName string
	flag.StringVar(&finderName, "finder", "finder", "Name of the finder service")
	var slotsName string
	flag.StringVar(&slotsName, "slots", "slots", "Name of the slots service")

	flag.Parse()

	if slotID == "" {
		slotID = generateID()
	}

	var slotAuth []byte
	if slotAuthHex != "" {
		var err error
		slotAuth, err = hex.DecodeString(slotAuthHex)
		if err != nil {
			log.Fatalf("Invalid slot-auth hex: %v", err)
		}
	}

	if discoveryURL == "" {
		log.Fatalf("A discovery service URL is required to find storage and slots services")
	}

	disc := discovery.NewClient(discoveryURL, nil)

	finderAddr := ""
	if finderName != "" {
		finderID, err := discovery.ResolveName(context.Background(), disc, finderName)
		if err != nil {
			log.Fatalf("Failed to resolve finder name %q: %v", finderName, err)
		}
		desc, ok := disc.Get(context.Background(), finderID)
		if !ok {
			log.Fatalf("Could not find address for Finder service %s", finderID)
		}
		finderAddr = desc.Address
	} else {
		log.Fatalf("A finder service name is required")
	}
	finderClient := finder.NewClient(finderAddr, nil)

	// Create storage client
	storageClient := storage.NewAggregateClient(finderClient, disc, 3, 1000)

	slotsAddr := ""
	if slotsName != "" {
		slotsID, err := discovery.ResolveName(context.Background(), disc, slotsName)
		if err != nil {
			log.Fatalf("Failed to resolve slots name %q: %v", slotsName, err)
		}
		desc, ok := disc.Get(context.Background(), slotsID)
		if !ok {
			log.Fatalf("Could not find address for Slots service %s", slotsID)
		}
		slotsAddr = desc.Address
	} else {
		log.Fatalf("A slots service name is required")
	}

	// Create slots client
	slotsClient := slots.NewClient(slotsAddr, nil)

	store, err := kv.NewStore(
		context.Background(),
		slotsClient,
		slotID,
		slotAuth,
		storageClient,
		dir,
		maxCacheSize,
		btreeThreshold,
		journalThreshold,
	)
	if err != nil {
		log.Fatalf("Failed to initialize KV store: %v", err)
	}
	defer store.Close()

	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", addr, err)
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port

	myID := generateID()
	err = discovery.AdvertiseAndRegister(context.Background(), disc, myID, advertiseAddr, actualPort, []string{"kv-v1"})
	if err != nil {
		log.Fatalf("Failed to register with discovery service: %v", err)
	}
	log.Printf("Registered with discovery service %s as %s", discoveryURL, myID)

	if name != "" {
		go func() {
			err := discovery.RegisterName(context.Background(), disc, name, myID, []string{"kv-v1"})
			if err != nil {
				log.Printf("Failed to register name %q: %v", name, err)
			} else {
				log.Printf("Registered name %q for ID %s", name, myID)
			}
		}()
	}

	server := kv.NewServer(store)
	log.Printf("KV service listening on :%d (Slot ID: %s)...", actualPort, slotID)
	log.Fatal(http.Serve(listener, server))
}
