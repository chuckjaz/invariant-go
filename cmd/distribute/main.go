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
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var repFactor int
	flag.IntVar(&repFactor, "N", 3, "Replication factor for blocks")
	flag.Parse()

	var disc discovery.Discovery
	if discoveryURL != "" {
		disc = discovery.NewClient(discoveryURL, nil)
	}

	d := distribute.NewInMemoryDistribute(disc, repFactor)
	if disc != nil {
		d.StartSync(10 * time.Second)
	}

	server := distribute.NewDistributeServer(d)

	port := os.Getenv("PORT")
	if port == "" {
		port = "3004"
	}

	addr := fmt.Sprintf(":%s", port)
	log.Printf("Distribute service listening on %s...", addr)
	log.Printf("Using In-Memory distribute storage")

	log.Fatal(http.ListenAndServe(addr, server))
}
