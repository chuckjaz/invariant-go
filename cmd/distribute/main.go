package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"invariant/internal/discovery"
	"invariant/internal/distribute"
)

func main() {
	var id string
	flag.StringVar(&id, "id", "", "ID of the distribute service (32-byte hex). Randomly generated if not provided.")
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var advertiseAddr string
	flag.StringVar(&advertiseAddr, "advertise", "", "Address to advertise to the discovery service")
	var repFactor int
	flag.IntVar(&repFactor, "N", 3, "Replication factor for blocks")
	flag.Parse()

	var disc discovery.Discovery
	if discoveryURL != "" {
		disc = discovery.NewClient(discoveryURL, nil)
	}

	d := distribute.NewInMemoryDistribute(disc, repFactor, 3)
	if disc != nil {
		d.StartSync(10 * time.Second)
	}

	server := distribute.NewDistributeServer(id, d)

	port := os.Getenv("PORT")
	if port == "" {
		port = "3004"
	}

	addr := fmt.Sprintf(":%s", port)

	if discoveryURL != "" {
		if advertiseAddr == "" {
			advertiseAddr = fmt.Sprintf("http://localhost:%s", port)
		}

		err := disc.Register(discovery.ServiceRegistration{
			ID:        server.ID(),
			Address:   advertiseAddr,
			Protocols: []string{"distribute-v1", "has-v1"},
		})
		if err != nil {
			log.Fatalf("Failed to register with discovery service: %v", err)
		}
		log.Printf("Registered with discovery service %s as %s", discoveryURL, server.ID())
	}

	log.Printf("Distribute service (ID %s) listening on %s...", server.ID(), addr)
	log.Printf("Using In-Memory distribute storage")

	log.Fatal(http.ListenAndServe(addr, server))
}
