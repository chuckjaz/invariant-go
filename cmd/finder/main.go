package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"

	"invariant/internal/discovery"
	"invariant/internal/finder"
)

func generateID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func main() {
	var id string
	flag.StringVar(&id, "id", "", "ID of the finder service (32-byte hex). Ramdomly generated if not provided.")
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var advertiseAddr string
	flag.StringVar(&advertiseAddr, "advertise", "", "Address to advertise to the discovery service")
	var port int
	flag.IntVar(&port, "port", 3004, "Port to listen on (using 3004 to not conflict with storage/discovery)")
	flag.Parse()

	if id == "" {
		id = generateID()
	}

	f, err := finder.NewMemoryFinder(id)
	if err != nil {
		log.Fatalf("Failed to create finder: %v", err)
	}

	addr := fmt.Sprintf(":%d", port)

	var disc discovery.Discovery
	if discoveryURL != "" {
		disc = discovery.NewClient(discoveryURL, nil)

		err := discovery.AdvertiseAndRegister(disc, id, advertiseAddr, port, []string{"finder-v1", "has-v1"})
		if err != nil {
			log.Fatalf("Failed to register with discovery service: %v", err)
		}
		log.Printf("Registered with discovery service %s as %s", discoveryURL, id)
	}

	server := finder.NewFinderServer(f, disc)

	log.Printf("Finder service (ID %s) listening on %s...", id, addr)
	log.Printf("Using In-Memory routing and storage mapping")

	log.Fatal(http.ListenAndServe(addr, server))
}
